// Command email-preview renders sample user emails without SMTP.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leha-creator/chanly/internal/integration/mail"
	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/notification/render"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func main() {
	out := flag.String("out", ".tmp/email-preview", "output directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string) error {
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	sender, err := mail.New(mail.Options{}, clock.NewManual(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)), logx.Discard())
	if err != nil {
		return err
	}
	subject, plain, html, err := sender.RenderLoginCode("123456", 10*time.Minute)
	if err != nil {
		return err
	}
	if err := save(out, "login_code", subject, plain, html); err != nil {
		return err
	}
	r, err := render.New("https://chanly.ru")
	if err != nil {
		return err
	}
	for _, linked := range []bool{true, false} {
		data := map[string]any{
			"channel_title": "Городские истории",
			"reason":        "Проверьте описание канала и количество подписчиков.\nПосле исправления отправьте канал на повторную проверку.",
		}
		name := "notification-no-link"
		if linked {
			data[domain.DataLink] = "/channels/example?tab=details&from=email"
			name = "notification"
		}
		subject, plain, html, err := r.Email(domain.Notification{Template: "channel.rejected", Payload: data})
		if err != nil {
			return err
		}
		if err := save(out, name, subject, plain, html); err != nil {
			return err
		}
	}
	return nil
}

func save(out, name, subject, plain, html string) error {
	for ext, body := range map[string]string{"subject.txt": subject, "txt": plain, "html": html} {
		if err := os.WriteFile(filepath.Join(out, name+"."+ext), []byte(body), 0644); err != nil {
			return err
		}
	}
	return nil
}
