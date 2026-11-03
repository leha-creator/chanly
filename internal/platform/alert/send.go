package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leha-creator/chanly/internal/platform/alert/sqlc"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// msk — время в тексте оповещения (A-12).
var msk = time.FixedZone("MSK", 3*60*60)

// Notice — оповещение к отправке.
type Notice struct {
	Level       string
	Reason      string
	ObjectKey   string
	Summary     string
	Fields      map[string]any
	RaisedCount int
	// FirstRaisedAt, LastRaisedAt — в UTC.
	FirstRaisedAt time.Time
	LastRaisedAt  time.Time
}

// Subject — тема письма: [ERROR] reason.
func (n Notice) Subject() string {
	return "[" + strings.ToUpper(n.Level) + "] " + n.Reason
}

// FieldLines — поля в виде «ключ: значение», по алфавиту.
func (n Notice) FieldLines() []string {
	keys := make([]string, 0, len(n.Fields))
	for k := range n.Fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := json.Marshal(n.Fields[k])
		if err != nil {
			v = []byte(fmt.Sprint(n.Fields[k]))
		}
		lines = append(lines, k+": "+string(v))
	}
	return lines
}

// LastRaisedAtMSK — время последнего срабатывания по Москве.
func (n Notice) LastRaisedAtMSK() string { return n.LastRaisedAt.In(msk).Format("02.01.2006 15:04") }

// FirstRaisedAtMSK — время первого срабатывания по Москве.
func (n Notice) FirstRaisedAtMSK() string { return n.FirstRaisedAt.In(msk).Format("02.01.2006 15:04") }

// Text — текст для чата дежурных.
func (n Notice) Text() string {
	var b strings.Builder
	b.WriteString(n.Subject())
	if n.ObjectKey != "" {
		b.WriteString(" (" + n.ObjectKey + ")")
	}
	b.WriteString("\n" + n.Summary)
	for _, l := range n.FieldLines() {
		b.WriteString("\n" + l)
	}
	fmt.Fprintf(&b, "\nСрабатываний: %d, последнее %s МСК", n.RaisedCount, n.LastRaisedAtMSK())
	s := b.String()
	if r := []rune(s); len(r) > 3900 { // предел сообщения MAX — 4000 символов
		s = string(r[:3900]) + "…"
	}
	return s
}

// Mailer отправляет письмо с оповещением. Реализация — integration/mail.
type Mailer interface {
	SendAlert(ctx context.Context, to string, n Notice) error
}

// ChatSender пишет в чат MAX. Реализация — integration/max.
type ChatSender interface {
	SendChatMessage(ctx context.Context, chatID int64, text string) (string, error)
}

// ErrChannelNotConfigured — для канала задания нет отправителя.
var ErrChannelNotConfigured = errors.New("alert channel not configured")

// Sender — обработчик задания send_alert.
type Sender struct {
	alerter *Alerter
	mailer  Mailer
	chat    ChatSender
}

// NewSender создаёт обработчик. mailer или chat могут быть nil.
func (a *Alerter) NewSender(mailer Mailer, chat ChatSender) *Sender {
	return &Sender{alerter: a, mailer: mailer, chat: chat}
}

// Register регистрирует send_alert в обработчике заданий.
func (s *Sender) Register(w *job.Worker) {
	w.Register(KindSendAlert, s.Send, job.HandlerOptions{NoFailureAlert: true})
}

// Send доставляет оповещение в один канал одному адресату.
func (s *Sender) Send(ctx context.Context, j job.Running) error {
	const op = "Alert.Send"
	log := s.alerter.log
	var p sendPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return job.Permanent(fmt.Errorf("decode payload: %w", err))
	}
	id, err := uuid.Parse(p.AlertID)
	if err != nil {
		return job.Permanent(fmt.Errorf("alert id: %w", err))
	}
	row, err := sqlc.New(s.alerter.pool).GetAlert(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		log.WarnContext(ctx, "alert to send not found", logx.KeyOp, op, "alert_id", p.AlertID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get alert: %w", err)
	}
	n := Notice{
		Level: row.Level, Reason: row.Reason, ObjectKey: row.ObjectKey, Summary: row.Summary, Fields: p.Fields,
		RaisedCount: int(row.RaisedCount), FirstRaisedAt: row.FirstRaisedAt, LastRaisedAt: row.LastRaisedAt,
	}
	switch p.Channel {
	case ChannelEmail:
		if s.mailer == nil {
			return job.Permanent(fmt.Errorf("%s: %w", p.Channel, ErrChannelNotConfigured))
		}
		if err := s.mailer.SendAlert(ctx, p.To, n); err != nil {
			return err
		}
	case ChannelMAX:
		if s.chat == nil {
			return job.Permanent(fmt.Errorf("%s: %w", p.Channel, ErrChannelNotConfigured))
		}
		chatID, err := strconv.ParseInt(p.To, 10, 64)
		if err != nil {
			return job.Permanent(fmt.Errorf("chat id: %w", err))
		}
		// Приоритет по умолчанию у адаптера MAX — ответы и оповещения, ниже публикаций (05 §2.8).
		if _, err := s.chat.SendChatMessage(ctx, chatID, n.Text()); err != nil {
			return err
		}
	default:
		return job.Permanent(fmt.Errorf("unknown alert channel %q", p.Channel))
	}
	log.InfoContext(ctx, "alert sent", logx.KeyOp, op, "reason", row.Reason, "object_key", row.ObjectKey, "channel", p.Channel)
	return nil
}
