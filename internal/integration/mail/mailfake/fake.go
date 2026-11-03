// Package mailfake — почта в памяти для тестов и разработки без SMTP (07 §9).
package mailfake

import (
	"context"
	"sync"
	"time"

	"github.com/leha-creator/chanly/internal/platform/alert"
)

// LoginCodeMessage — отправленное письмо с кодом входа.
type LoginCodeMessage struct {
	To        string
	Code      string
	ExpiresIn time.Duration
}

// Sender запоминает письма вместо отправки.
type Sender struct {
	mu      sync.Mutex
	sent    []LoginCodeMessage
	alerts  []AlertMessage
	failErr error

	notifications []NotificationMessage
	notifyErr     error
}

// New создаёт пустой Sender.
func New() *Sender { return &Sender{} }

// SendLoginCode запоминает письмо или возвращает ошибку, заданную FailWith.
func (s *Sender) SendLoginCode(ctx context.Context, email, code string, expiresIn time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return s.failErr
	}
	s.sent = append(s.sent, LoginCodeMessage{To: email, Code: code, ExpiresIn: expiresIn})
	return nil
}

// FailWith заставляет следующие отправки возвращать err; nil — снова отправлять.
func (s *Sender) FailWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failErr = err
}

// Last — последнее письмо на адрес.
func (s *Sender) Last(email string) (LoginCodeMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sent) - 1; i >= 0; i-- {
		if s.sent[i].To == email {
			return s.sent[i], true
		}
	}
	return LoginCodeMessage{}, false
}

// Count — сколько писем отправлено.
func (s *Sender) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// AlertMessage — отправленное письмо с оповещением.
type AlertMessage struct {
	To     string
	Notice alert.Notice
}

// SendAlert запоминает оповещение или возвращает ошибку, заданную FailWith.
func (s *Sender) SendAlert(_ context.Context, to string, n alert.Notice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return s.failErr
	}
	s.alerts = append(s.alerts, AlertMessage{To: to, Notice: n})
	return nil
}

// Alerts возвращает копию отправленных оповещений.
func (s *Sender) Alerts() []AlertMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AlertMessage(nil), s.alerts...)
}

// NotificationMessage — отправленное письмо уведомления.
type NotificationMessage struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// SendNotification запоминает письмо уведомления или возвращает ошибку FailNotificationsWith
// (или общую FailWith).
func (s *Sender) SendNotification(_ context.Context, to, subject, text, html string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notifyErr != nil {
		return s.notifyErr
	}
	if s.failErr != nil {
		return s.failErr
	}
	s.notifications = append(s.notifications, NotificationMessage{To: to, Subject: subject, Text: text, HTML: html})
	return nil
}

// FailNotificationsWith ломает только письма уведомлений; nil — снова отправлять.
func (s *Sender) FailNotificationsWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifyErr = err
}

// Notifications — копия отправленных писем уведомлений.
func (s *Sender) Notifications() []NotificationMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]NotificationMessage(nil), s.notifications...)
}
