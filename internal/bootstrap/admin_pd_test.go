package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
)

// Этап «API администратора»: запросы субъекта ПДн — выгрузка и обезличивание (R-26).

func (a *testApp) pdAdmin(args ...string) (string, error) {
	a.t.Helper()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	var out bytes.Buffer
	err := runAdmin(context.Background(), cfg, a.mods.in.log, append([]string{"pd"}, args...), &out)
	return out.String(), err
}

func TestAdminPersonalData(t *testing.T) {
	a := newTestApp(t)
	a.publishAll()
	const email = "subject.pd@example.ru"
	adv := a.newAdvertiser(email)
	a.acceptOffers(adv)
	senior := a.adminClient("senior.pd@example.ru", accountdomain.AdminSenior)
	a.adminClient("mod.pd@example.ru", accountdomain.AdminModerator)
	orgID := a.orgOf(email)
	var inn, name string
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT inn, name FROM organization WHERE id = $1`, orgID).Scan(&inn, &name); err != nil {
		t.Fatal(err)
	}

	// Только senior; без основания обезличивание не запускается.
	if _, err := a.pdAdmin("export", "--email", email, "--operator-email", "mod.pd@example.ru"); !errors.Is(err, errNotSenior) {
		t.Fatalf("moderator export: %v", err)
	}
	if _, err := a.pdAdmin("anonymize", "--email", email, "--operator-email", "senior.pd@example.ru"); !errors.Is(err, errUsage) {
		t.Fatalf("anonymize without reason: %v", err)
	}
	if _, err := a.pdAdmin("export", "--email", "nobody@example.ru", "--operator-email", "senior.pd@example.ru"); err == nil {
		t.Fatal("export of unknown subject")
	}

	// Выгрузка: учётная запись, организация с полным ИНН, принятия, сессии.
	out, err := a.pdAdmin("export", "--email", email, "--operator-email", "senior.pd@example.ru")
	if err != nil {
		t.Fatalf("export: %v %s", err, out)
	}
	var exp struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
		Organization *struct {
			INN     string `json:"inn"`
			Members int    `json:"members"`
		} `json:"organization"`
		Acceptances []struct {
			Document string `json:"document"`
		} `json:"legal_acceptances"`
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &exp); err != nil {
		t.Fatalf("export json: %v %s", err, out)
	}
	if exp.User.Email != email || exp.Organization == nil || exp.Organization.INN != inn || exp.Organization.Members != 1 ||
		len(exp.Acceptances) == 0 || len(exp.Sessions) == 0 {
		t.Fatalf("export %+v", exp)
	}

	// Обезличивание: почта заменена, сессии завершены, контакты единственного участника стёрты,
	// ИНН и наименование на месте; повтор ничего не меняет.
	out, err = a.pdAdmin("anonymize", "--email", email, "--reason", "запрос субъекта от 2026-10-20", "--operator-email", "senior.pd@example.ru")
	if err != nil || !strings.Contains(out, "anonymized") || !strings.Contains(out, "organization contacts cleared true") {
		t.Fatalf("anonymize: %v %s", err, out)
	}
	expectStatus(t, adv.do(http.MethodGet, "/me", nil), http.StatusUnauthorized, "anonymized session")
	// Разблокировать обезличенного нельзя: 409, а не 200 без изменений.
	var subjectID string
	if err := a.db.Pool.QueryRow(context.Background(), `SELECT user_id::text FROM organization_member WHERE organization_id = $1`, orgID).Scan(&subjectID); err != nil {
		t.Fatal(err)
	}
	r := senior.do(http.MethodPost, "/admin/users/"+subjectID+"/unblock", map[string]any{"reason": "ошибка"})
	if _, d := r.errorCode(t); r.status != http.StatusConflict || d["reason"] != "anonymized" {
		t.Fatalf("unblock anonymized: %d %s", r.status, r.body)
	}
	if n := a.count(`SELECT count(*) FROM "user" WHERE email = $1`, email); n != 0 {
		t.Fatalf("old email kept: %d", n)
	}
	if n := a.count(`SELECT count(*) FROM organization WHERE id = $1 AND inn = $2 AND name = $3 AND contact_name = '' AND phone = ''`,
		orgID, inn, name); n != 1 {
		t.Fatal("organization contacts not cleared or accounting data lost")
	}
	if n := a.count(`SELECT count(*) FROM session s JOIN organization_member m ON m.user_id = s.user_id
		WHERE m.organization_id = $1 AND (s.ip IS NOT NULL OR s.revoked_at IS NULL)`, orgID); n != 0 {
		t.Fatalf("sessions not cleared: %d", n)
	}
	if n := a.count(`SELECT count(*) FROM auth_code WHERE email = $1`, email); n != 0 {
		t.Fatalf("login codes kept: %d", n)
	}
	if n := a.count(`SELECT count(*) FROM audit_log WHERE action IN ('user.anonymized', 'personal_data.exported') AND actor_type = 'admin'
		AND reason IS DISTINCT FROM ''`); n != 2 {
		t.Fatalf("audit %d", n)
	}
	if out, err := a.pdAdmin("anonymize", "--email", email, "--reason", "повтор", "--operator-email", "senior.pd@example.ru"); err == nil {
		t.Fatalf("repeat by old email must not find the subject: %s", out)
	}
	// Администратора не обезличить.
	if _, err := a.pdAdmin("anonymize", "--email", "mod.pd@example.ru", "--reason", "x", "--operator-email", "senior.pd@example.ru"); err == nil {
		t.Fatal("admin anonymized")
	}

	// Организация с двумя участниками: контакты не трогаются.
	const second = "subject2.pd@example.ru"
	a.newAdvertiser(second)
	org2 := a.orgOf(second)
	other := uuid.New()
	now := time.Now()
	if _, err := a.db.Pool.Exec(context.Background(), `INSERT INTO "user" (id, email, status, created_at, updated_at) VALUES ($1, 'colleague.pd@example.ru', 'active', $2, $2)`, other, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Pool.Exec(context.Background(), `INSERT INTO organization_member (id, organization_id, user_id, role, created_at, updated_at) VALUES ($1, $2, $3, 'owner', $4, $4)`,
		uuid.New(), org2, other, now); err != nil {
		t.Fatal(err)
	}
	out, err = a.pdAdmin("anonymize", "--email", second, "--reason", "запрос", "--operator-email", "senior.pd@example.ru")
	if err != nil || !strings.Contains(out, "organization contacts cleared false") {
		t.Fatalf("anonymize member: %v %s", err, out)
	}
	if n := a.count(`SELECT count(*) FROM organization WHERE id = $1 AND contact_name <> '' AND phone <> ''`, org2); n != 1 {
		t.Fatal("contacts of a shared organization cleared")
	}

	// В журнале приложения нет почты и ИНН субъекта.
	for _, secret := range []string{email, second, inn} {
		if bytes.Contains(a.logs.Bytes(), []byte(secret)) {
			t.Fatalf("log contains %s", secret)
		}
	}
}
