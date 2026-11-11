package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Чего не хватает каналу для одобрения (R-18, details.missing).
const (
	MissingRights   = "rights"
	MissingCategory = "category"
	MissingPrice    = "price"
	MissingRkn      = "rkn"
)

// RejectReasonMaxLength — предел текста причины отказа модерации (R-1).
const RejectReasonMaxLength = 500

var (
	// ErrNotInModeration — одобрить или отклонить можно только канал в moderation (R-18).
	ErrNotInModeration = errors.New("channel: not in moderation")
	// ErrApproveBlocked — каналу не хватает условий одобрения; подробности — *ApproveBlockedError.
	ErrApproveBlocked = errors.New("channel: approve requirements are not met")
	// ErrRknNotCheckable — номер РКН нельзя проверить: канал не подключён или номера нет.
	ErrRknNotCheckable = errors.New("channel: rkn number cannot be checked")
	// ErrRejectReason — причина отказа пуста или длиннее RejectReasonMaxLength.
	ErrRejectReason = &RuleError{Field: "reason", Reason: "length"}
)

// ApproveBlockedError — чего не хватает для одобрения, в порядке rights, category, price, rkn.
type ApproveBlockedError struct{ Missing []string }

func (e *ApproveBlockedError) Error() string {
	return ErrApproveBlocked.Error() + ": " + strings.Join(e.Missing, ", ")
}

func (e *ApproveBlockedError) Is(target error) bool { return target == ErrApproveBlocked }

// ApproveMissing — условия одобрения, которых нет у канала (S-16, R-18).
func (c *Channel) ApproveMissing(rknThreshold int) []string {
	missing := []string{}
	if c.problem() != "" {
		missing = append(missing, MissingRights)
	}
	if c.CategoryID == nil {
		missing = append(missing, MissingCategory)
	}
	if !c.HasOfferedPrice() {
		missing = append(missing, MissingPrice)
	}
	if c.RknRequired(rknThreshold) {
		missing = append(missing, MissingRkn)
	}
	return missing
}

// Approve — администратор одобряет канал (S-16 шаг 3): moderation → active.
func (c *Channel) Approve(adminID uuid.UUID, rknThreshold int, now time.Time) (StatusChange, error) {
	if c.Status != StatusModeration {
		return StatusChange{}, ErrNotInModeration
	}
	if missing := c.ApproveMissing(rknThreshold); len(missing) > 0 {
		return StatusChange{}, &ApproveBlockedError{Missing: missing}
	}
	change := StatusChange{ChannelID: c.ID, From: c.Status, To: StatusActive}
	c.Status, c.StatusReason = StatusActive, ""
	c.ModeratedAt, c.ModeratedBy = &now, &adminID
	c.touch(now)
	return change, nil
}

// Reject — администратор отклоняет канал (S-16): moderation → rejected, текст причины
// виден блогеру в status_reason. После правки блогером канал снова на модерации.
func (c *Channel) Reject(reason string, now time.Time) (StatusChange, error) {
	if c.Status != StatusModeration {
		return StatusChange{}, ErrNotInModeration
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > RejectReasonMaxLength {
		return StatusChange{}, ErrRejectReason
	}
	change := StatusChange{ChannelID: c.ID, From: c.Status, To: StatusRejected, Reason: reason}
	c.Status, c.StatusReason = StatusRejected, reason
	c.touch(now)
	return change, nil
}

// RknCheckResult — итог ручной проверки номера в реестре РКН (06 §6.3, Q-34).
type RknCheckResult string

const (
	RknCheckConfirmed RknCheckResult = "confirmed"
	RknCheckRejected  RknCheckResult = "rejected"
)

// RknChecked — администратор сверил номер с реестром (R-18): состояние проверки,
// кто и когда; затем правило РКН (R-16) возвращает канал из rkn_required или оставляет.
func (c *Channel) RknChecked(result RknCheckResult, adminID uuid.UUID, rknThreshold int, now time.Time) (StatusChange, error) {
	if c.Status == StatusUnclaimed || c.Status == StatusDisconnected || c.RknNumber == "" {
		return StatusChange{}, ErrRknNotCheckable
	}
	switch result {
	case RknCheckConfirmed:
		c.RknStatus = RknConfirmed
	case RknCheckRejected:
		c.RknStatus = RknRejected
	default:
		return StatusChange{}, &RuleError{Field: "result", Reason: "unknown"}
	}
	c.RknCheckedAt, c.RknCheckedBy = &now, &adminID
	return c.resolve(rknThreshold, now), nil
}

// RknRecheckDue — номер подтверждён раньше, чем interval назад: администратору пора
// проверить его снова (06 §6.3, R-17). Канал при этом продолжает работать.
func (c *Channel) RknRecheckDue(now time.Time, interval time.Duration) bool {
	return c.RknStatus == RknConfirmed && c.RknCheckedAt != nil && now.Sub(*c.RknCheckedAt) > interval
}

// Блокировка канала администратором (R-7 этапа «API администратора»).
var (
	// ErrAlreadyBlocked — канал уже заблокирован: 409.
	ErrAlreadyBlocked = errors.New("channel: already blocked")
	// ErrNotBlocked — снять блокировку можно только с заблокированного канала: 409.
	ErrNotBlocked = errors.New("channel: not blocked")
)

// ReasonBlockedByAdmin — причина состояния blocked для блогера; основание — в audit_log.
const ReasonBlockedByAdmin = "blocked_by_admin"

// Block — администратор блокирует канал (R-7): канал уходит из каталога и не принимает новые
// размещения; начатые продолжаются (смена не считается потерей доступности, S-25 не применяется).
func (c *Channel) Block(now time.Time) (StatusChange, error) {
	if c.Status == StatusBlocked {
		return StatusChange{}, ErrAlreadyBlocked
	}
	change := StatusChange{ChannelID: c.ID, From: c.Status, To: StatusBlocked, Reason: ReasonBlockedByAdmin}
	c.Status, c.StatusReason, c.StatusBeforeDisconnect = StatusBlocked, ReasonBlockedByAdmin, nil
	c.touch(now)
	return change, nil
}

// Unblock — снятие блокировки (R-7): канал снова проходит модерацию; без прав — pending_rights.
func (c *Channel) Unblock(rknThreshold int, now time.Time) (StatusChange, error) {
	if c.Status != StatusBlocked {
		return StatusChange{}, ErrNotBlocked
	}
	c.Status, c.StatusReason = StatusModeration, ""
	change := c.resolve(rknThreshold, now)
	change.From = StatusBlocked
	return change, nil
}

// Справочник категорий (R-8 этапа «API администратора»).
var (
	ErrCategorySlug = &RuleError{Field: "slug", Reason: "invalid"}
	ErrCategoryName = &RuleError{Field: "name", Reason: "length"}
	// ErrCategoryExists — slug уже занят: 409.
	ErrCategoryExists = errors.New("channel: category slug already exists")
)

// CategoryInput — новая категория или правка.
type CategoryInput struct {
	Slug     string
	Name     string
	Position *int
	Archived *bool
}

// CheckSlug — латиница в нижнем регистре, цифры, «-» и «_», до 50 символов.
func CheckSlug(slug string) error {
	if n := len(slug); n == 0 || n > 50 {
		return ErrCategorySlug
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ErrCategorySlug
		}
	}
	return nil
}

// CheckCategoryName — название категории 1…100 символов.
func CheckCategoryName(name string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n == 0 || n > 100 {
		return ErrCategoryName
	}
	return nil
}
