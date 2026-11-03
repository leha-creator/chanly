package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// User — пользователь. Единственный идентификатор для входа — почта (A-13).
type User struct {
	ID          uuid.UUID
	Email       string
	AdminRole   *AdminRole
	Status      Status
	LastLoginAt *time.Time
	// BlockedAt и BlockedReason — у заблокированного администратором (R-12).
	BlockedAt     *time.Time
	BlockedReason *string
	// AnonymizedAt — данные пользователя обезличены по запросу субъекта (R-26).
	AnonymizedAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewUser создаёт пользователя при первом входе.
func NewUser(id uuid.UUID, email string, now time.Time) *User {
	return &User{ID: id, Email: NormalizeEmail(email), Status: StatusActive, CreatedAt: now, UpdatedAt: now}
}

// IsAdmin сообщает, что у пользователя есть роль администратора.
func (u *User) IsAdmin() bool { return u.AdminRole != nil }

// Blocked сообщает, что пользователь заблокирован.
func (u *User) Blocked() bool { return u.Status == StatusBlocked }

// NormalizeEmail приводит адрес к виду, в котором он хранится и хешируется.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ReasonAdminUser — блокировать администратора нельзя: сначала снимается роль (R-12).
const ReasonAdminUser = "admin_user"

// Block блокирует пользователя (этап «API администратора», R-12). Администратора — отказ
// admin_user; повтор блокировки ничего не меняет (false).
func (u *User) Block(reason string, now time.Time) (bool, error) {
	if u.IsAdmin() {
		return false, apperr.Conflict("admin user cannot be blocked").WithDetail("reason", ReasonAdminUser)
	}
	if u.Blocked() {
		return false, nil
	}
	u.Status, u.BlockedAt, u.BlockedReason, u.UpdatedAt = StatusBlocked, &now, &reason, now
	return true, nil
}

// Unblock снимает блокировку; у активного и обезличенного пользователя ничего не меняет (false).
func (u *User) Unblock(now time.Time) bool {
	// Обезличенного не разблокировать: учётная запись без почты (R-26).
	if !u.Blocked() || u.AnonymizedAt != nil {
		return false
	}
	u.Status, u.BlockedAt, u.BlockedReason, u.UpdatedAt = StatusActive, nil, nil, now
	return true
}

// ReasonPersonalDataRequest — основание блокировки при обезличивании (152-ФЗ, R-26).
const ReasonPersonalDataRequest = "personal_data_request"

// AnonymizedEmail — почта обезличенного пользователя: уникальна, писем не получает (домен .invalid).
func AnonymizedEmail(id uuid.UUID) string { return "anonymized-" + id.String() + "@anonymized.invalid" }

// Anonymize обезличивает учётную запись (этап «API администратора», R-26): почта заменяется,
// пользователь блокируется. Администратора — отказ admin_user; повтор ничего не меняет (false).
func (u *User) Anonymize(now time.Time) (bool, error) {
	if u.IsAdmin() {
		return false, apperr.Conflict("admin user cannot be anonymized").WithDetail("reason", ReasonAdminUser)
	}
	if u.AnonymizedAt != nil {
		return false, nil
	}
	if !u.Blocked() {
		reason := ReasonPersonalDataRequest
		u.Status, u.BlockedAt, u.BlockedReason = StatusBlocked, &now, &reason
	}
	u.Email, u.AnonymizedAt, u.UpdatedAt = AnonymizedEmail(u.ID), &now, now
	return true, nil
}
