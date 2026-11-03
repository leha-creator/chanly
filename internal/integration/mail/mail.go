// Package mail — отправка писем через SMTP (A-13, A-19). Вызывается только из
// обработчиков заданий (07 §1). Тексты — шаблоны из templates/email (07 §6).
package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"log/slog"
	"strings"
	"text/template"
	"time"

	gomail "github.com/wneessen/go-mail"

	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/templates"
)

// sendTimeout — предел одной отправки.
const sendTimeout = 10 * time.Second

// msk — московское время для текста писем (README «Время»).
var msk = time.FixedZone("MSK", 3*60*60)

// Options — подключение к SMTP.
type Options struct {
	Host     string
	Port     int
	User     string
	Password string
	// TLS: starttls, tls или none.
	TLS  string
	From string
}

// Sender отправляет письма.
type Sender struct {
	opts  Options
	clock clock.Clock
	log   *slog.Logger
	tpl   *rendered
	alert *rendered
}

// New создаёт Sender и проверяет, что шаблоны разбираются.
func New(opts Options, clk clock.Clock, log *slog.Logger) (*Sender, error) {
	tpl, err := loadTemplates("login_code")
	if err != nil {
		return nil, err
	}
	alertTpl, err := loadTemplates("alert")
	if err != nil {
		return nil, err
	}
	return &Sender{opts: opts, clock: clk, log: log, tpl: tpl, alert: alertTpl}, nil
}

// RenderAlert возвращает тему, текст и HTML письма с оповещением.
func (s *Sender) RenderAlert(n alert.Notice) (subject, text, html string, err error) {
	return s.alert.render(n)
}

// SendAlert отправляет оповещение администратору (реализует alert.Mailer, 07 §3).
func (s *Sender) SendAlert(ctx context.Context, to string, n alert.Notice) error {
	subject, text, html, err := s.RenderAlert(n)
	if err != nil {
		return err
	}
	return s.send(ctx, "Mail.SendAlert", "alert", to, subject, text, html)
}

// LoginCodeData — данные шаблона письма с кодом.
type LoginCodeData struct {
	Code           string
	ExpiresMinutes int
	ExpiresAtMSK   string
}

// RenderLoginCode возвращает тему, текст и HTML письма с кодом.
func (s *Sender) RenderLoginCode(code string, expiresIn time.Duration) (subject, text, html string, err error) {
	data := LoginCodeData{
		Code:           code,
		ExpiresMinutes: int((expiresIn + time.Minute - 1) / time.Minute),
		ExpiresAtMSK:   s.clock.Now().Add(expiresIn).In(msk).Format("15:04"),
	}
	return s.tpl.render(data)
}

// SendLoginCode отправляет письмо с кодом входа (реализует domain.LoginCodeSender).
func (s *Sender) SendLoginCode(ctx context.Context, email, code string, expiresIn time.Duration) error {
	const op = "Mail.SendLoginCode"
	subject, text, html, err := s.RenderLoginCode(code, expiresIn)
	if err != nil {
		return err
	}
	return s.send(ctx, op, "login_code", email, subject, text, html)
}

// errInvalidAddress — адрес получателя не разбирается.
var errInvalidAddress = errors.New("mail to: invalid address")

func (s *Sender) send(ctx context.Context, op, template, to, subject, text, html string) error {
	masked := logx.MaskEmail(to)
	s.log.DebugContext(ctx, "mail send started", logx.KeyOp, op, "template", template, "to", masked)
	start := s.clock.Now()

	m := gomail.NewMsg()
	if err := m.From(s.opts.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := m.To(to); err != nil {
		return errInvalidAddress
	}
	m.Subject(subject)
	m.SetBodyString(gomail.TypeTextPlain, text)
	m.AddAlternativeString(gomail.TypeTextHTML, html)

	client, err := gomail.NewClient(s.opts.Host, s.clientOptions()...)
	if err != nil {
		return fmt.Errorf("mail client: %w", err)
	}
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := client.DialAndSendWithContext(sctx, m); err != nil {
		// Текст ошибки SMTP не содержит учётных данных: их go-mail не выводит.
		// Отказ RCPT у go-mail называет адресата: в журнал и выше — только с маской (07 §5.4).
		err = redacted{err: err, text: strings.ReplaceAll(err.Error(), to, masked)}
		s.log.ErrorContext(ctx, "mail send failed", logx.KeyOp, op, "template", template, "to", masked, logx.KeyError, err.Error())
		return fmt.Errorf("send mail: %w", err)
	}
	s.log.InfoContext(ctx, "mail sent", logx.KeyOp, op, "template", template,
		"duration_ms", s.clock.Now().Sub(start).Milliseconds())
	return nil
}

func (s *Sender) clientOptions() []gomail.Option {
	opts := []gomail.Option{gomail.WithPort(s.opts.Port), gomail.WithTimeout(sendTimeout)}
	switch s.opts.TLS {
	case "tls":
		opts = append(opts, gomail.WithSSL())
	case "none":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	if s.opts.User != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
			gomail.WithUsername(s.opts.User), gomail.WithPassword(s.opts.Password))
	}
	return opts
}

// rendered — набор шаблонов одного письма: тема, текст, HTML.
type rendered struct {
	subject *template.Template
	text    *template.Template
	html    *htmltemplate.Template
}

func loadTemplates(name string) (*rendered, error) {
	read := func(part string) (string, error) {
		b, err := templates.FS.ReadFile("email/" + name + "." + part + ".tmpl")
		if err != nil {
			return "", fmt.Errorf("template %s.%s: %w", name, part, err)
		}
		return string(b), nil
	}
	var r rendered
	src, err := read("subject")
	if err != nil {
		return nil, err
	}
	if r.subject, err = template.New("subject").Option("missingkey=error").Parse(src); err != nil {
		return nil, err
	}
	if src, err = read("txt"); err != nil {
		return nil, err
	}
	if r.text, err = template.New("text").Option("missingkey=error").Parse(src); err != nil {
		return nil, err
	}
	if src, err = read("html"); err != nil {
		return nil, err
	}
	if r.html, err = htmltemplate.New("html").Option("missingkey=error").Parse(src); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *rendered) render(data any) (subject, text, html string, err error) {
	var b bytes.Buffer
	if err = r.subject.Execute(&b, data); err != nil {
		return "", "", "", err
	}
	subject = strings.TrimSpace(b.String())
	b.Reset()
	if err = r.text.Execute(&b, data); err != nil {
		return "", "", "", err
	}
	text = b.String()
	b.Reset()
	if err = r.html.Execute(&b, data); err != nil {
		return "", "", "", err
	}
	return subject, text, b.String(), nil
}

// SendNotification отправляет письмо уведомления (07 §6); тексты собирает модуль уведомлений
// (notification/render). Отказ адреса (неверный адрес, постоянная ошибка RCPT TO) помечен
// job.Permanent: повтор его не исправит.
// ПРЕДПОЛОЖЕНИЕ: SMTP отклоняет несуществующий адрес синхронно кодом 5xx на RCPT TO; отчёты о
// недоставке (bounce) не обрабатываются (E-6).
func (s *Sender) SendNotification(ctx context.Context, to, subject, text, html string) error {
	err := s.send(ctx, "Mail.SendNotification", "notification", to, subject, text, html)
	if err == nil {
		return nil
	}
	var se *gomail.SendError
	if errors.As(err, &se) && se.Reason == gomail.ErrSMTPRcptTo && !se.IsTemp() {
		return job.Permanent(err)
	}
	if errors.Is(err, errInvalidAddress) {
		return job.Permanent(err)
	}
	return err
}

// redacted — ошибка с текстом без адреса получателя; причина доступна через errors.As.
type redacted struct {
	err  error
	text string
}

func (r redacted) Error() string { return r.text }
func (r redacted) Unwrap() error { return r.err }
