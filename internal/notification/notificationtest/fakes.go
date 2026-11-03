package notificationtest

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
)

// Recipients — адресаты в памяти.
type Recipients struct {
	mu      sync.Mutex
	members map[uuid.UUID][]uuid.UUID
	admins  []uuid.UUID
	emails  map[uuid.UUID]string
}

// NewRecipients создаёт пустой справочник адресатов.
func NewRecipients() *Recipients {
	return &Recipients{members: map[uuid.UUID][]uuid.UUID{}, emails: map[uuid.UUID]string{}}
}

// AddMember добавляет пользователя организации с почтой.
func (r *Recipients) AddMember(org, user uuid.UUID, email string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[org] = append(r.members[org], user)
	r.emails[user] = email
}

// AddAdmin добавляет администратора.
func (r *Recipients) AddAdmin(user uuid.UUID, email string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.admins = append(r.admins, user)
	r.emails[user] = email
}

// SetEmail меняет почту; пусто — пользователь заблокирован.
func (r *Recipients) SetEmail(user uuid.UUID, email string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emails[user] = email
}

func (r *Recipients) Members(_ context.Context, org uuid.UUID) ([]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.members[org]...), nil
}

func (r *Recipients) Admins(context.Context) ([]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.admins...), nil
}

func (r *Recipients) Email(_ context.Context, user uuid.UUID) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.emails[user], nil
}

// Channels — каналы в памяти.
type Channels map[uuid.UUID]domain.ChannelInfo

func (c Channels) Channel(_ context.Context, id uuid.UUID) (domain.ChannelInfo, error) {
	info, ok := c[id]
	if !ok {
		return domain.ChannelInfo{}, domain.ErrNotFound
	}
	return info, nil
}

// Sent — отправленное сообщение или письмо.
type Sent struct {
	To      string
	MaxUser int64
	Subject string
	Text    string
}

// Delivery — поддельные бот и почта: запоминают отправленное, отказ задаётся Fail*.
type Delivery struct {
	mu       sync.Mutex
	messages []Sent
	mails    []Sent
	maxErr   error
	mailErr  error
}

// FailMAX задаёт ошибку личных сообщений; nil — снова работать.
func (d *Delivery) FailMAX(err error) { d.mu.Lock(); d.maxErr = err; d.mu.Unlock() }

// FailMail задаёт ошибку писем; nil — снова работать.
func (d *Delivery) FailMail(err error) { d.mu.Lock(); d.mailErr = err; d.mu.Unlock() }

func (d *Delivery) SendUserMessage(_ context.Context, user int64, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.maxErr != nil {
		return d.maxErr
	}
	d.messages = append(d.messages, Sent{MaxUser: user, Text: text})
	return nil
}

func (d *Delivery) SendNotification(_ context.Context, to, subject, text, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mailErr != nil {
		return d.mailErr
	}
	d.mails = append(d.mails, Sent{To: to, Subject: subject, Text: text})
	return nil
}

// Messages — личные сообщения бота.
func (d *Delivery) Messages() []Sent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Sent(nil), d.messages...)
}

// Mails — письма.
func (d *Delivery) Mails() []Sent {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Sent(nil), d.mails...)
}
