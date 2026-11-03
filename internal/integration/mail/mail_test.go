package mail_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leha-creator/chanly/internal/integration/mail"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// MailpitImage — тестовый SMTP-сервер (тот же, что в compose.dev.yml).
const MailpitImage = "axllent/mailpit:v1.31.5"

func TestRenderLoginCode(t *testing.T) {
	clk := clock.NewManual(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	s, err := mail.New(mail.Options{}, clk, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	subject, text, html, err := s.RenderLoginCode("123456", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Код входа: 123456" {
		t.Fatalf("subject %q", subject)
	}
	for _, body := range []string{text, html} {
		if !strings.Contains(body, "123456") || !strings.Contains(body, "10 мин.") || !strings.Contains(body, "12:10 МСК") {
			t.Fatalf("body:\n%s", body)
		}
	}
}

func TestSendThroughMailpit(t *testing.T) {
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, MailpitImage,
		testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/livez").WithPort("8025/tcp").WithStartupTimeout(time.Minute),
			wait.ForListeningPort("1025/tcp"),
			wait.ForLog("[smtpd] starting on")),
	)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("Mailpit is required in CI: %v", err)
		}
		t.Skipf("Docker is not available, Mailpit test is skipped: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	smtpPort, _ := ctr.MappedPort(ctx, "1025/tcp")
	smtp, _ := strconv.Atoi(smtpPort.Port())
	webPort, _ := ctr.MappedPort(ctx, "8025/tcp")
	if err := smtpGreeting(net.JoinHostPort(host, smtpPort.Port())); err != nil {
		// На части рабочих мест SMTP через проброс порта Docker перехватывает
		// локальное ПО (например, антивирус): приветствие сервера не доходит.
		if os.Getenv("CI") == "true" {
			t.Fatalf("Mailpit SMTP is unreachable: %v", err)
		}
		t.Skipf("SMTP greeting through Docker port mapping is blocked on this machine: %v", err)
	}

	s, err := mail.New(mail.Options{Host: host, Port: smtp, TLS: "none", From: "noreply@maxim.test"},
		clock.System{}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendLoginCode(ctx, "user@example.ru", "654321", 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s:%s/api/v1/messages", host, webPort.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var list struct {
		Messages []struct {
			Subject string `json:"Subject"`
			To      []struct {
				Address string `json:"Address"`
			} `json:"To"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("mailpit response: %v %s", err, raw)
	}
	if len(list.Messages) != 1 || list.Messages[0].Subject != "Код входа: 654321" || list.Messages[0].To[0].Address != "user@example.ru" {
		t.Fatalf("messages: %s", raw)
	}
}

// smtpGreeting проверяет, что сервер присылает приветствие 220.
func smtpGreeting(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "220") {
		return fmt.Errorf("unexpected greeting %q", line)
	}
	return nil
}

func TestRenderAlert(t *testing.T) {
	clk := clock.NewManual(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	s, err := mail.New(mail.Options{}, clk, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	n := alert.Notice{Level: "error", Reason: "job_failed:sync_ord_object", ObjectKey: "job-1", Summary: "Job failed after 10 attempts",
		Fields: map[string]any{"attempts": 10, "kind": "sync_ord_object"}, RaisedCount: 2,
		FirstRaisedAt: clk.Now(), LastRaisedAt: clk.Now().Add(time.Hour)}
	subject, text, html, err := s.RenderAlert(n)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "[ERROR] job_failed:sync_ord_object" {
		t.Fatalf("subject %q", subject)
	}
	for _, body := range []string{text, html} {
		for _, want := range []string{"job-1", "Job failed after 10 attempts", "kind: ", "attempts: 10", "13:00 МСК"} {
			if !strings.Contains(body, want) {
				t.Fatalf("body has no %q:\n%s", want, body)
			}
		}
	}
}

// Неверный адрес — отказ без повторов: письмо не уходит, сервер не нужен.
func TestSendNotificationInvalidAddressIsPermanent(t *testing.T) {
	s, err := mail.New(mail.Options{Host: "127.0.0.1", Port: 1, TLS: "none", From: "noreply@maxim.test"}, clock.System{}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	err = s.SendNotification(context.Background(), "not an address", "Тема", "текст", "<p>текст</p>")
	if !job.IsPermanent(err) {
		t.Fatalf("invalid address must be permanent: %v", err)
	}
}
