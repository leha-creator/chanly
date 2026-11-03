package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"time"

	accountapp "github.com/leha-creator/chanly/internal/account/app"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// runAdminPD — запросы субъекта персональных данных (152-ФЗ; этап «API администратора», R-26),
// см. docs/launch/personal-data.md:
//
//	maxim admin pd export --email <субъект> --operator-email <senior>
//	maxim admin pd anonymize --email <субъект> --reason <основание> --operator-email <senior>
//
// Обе команды — только senior; выгрузка (JSON) — в stdout для ответа субъекту, в журнал
// приложения пишутся только идентификаторы. Обе пишутся в audit_log.
func runAdminPD(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	const op = "Bootstrap.AdminPD"
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet("admin pd "+args[0], flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		email    = fs.String("email", "", "subject email")
		operator = fs.String("operator-email", "", "senior admin email")
		reason   = fs.String("reason", "", "reason (subject request)")
	)
	if err := fs.Parse(args[1:]); err != nil || *email == "" || *operator == "" {
		return errUsage
	}
	switch args[0] {
	case "export":
	case "anonymize":
		if *reason == "" {
			return errUsage
		}
	default:
		log.ErrorContext(ctx, "unknown admin pd command", logx.KeyOp, op, "command", args[0])
		return errUsage
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	user, err := accountpg.New(pool, log).Users().GetByEmail(ctx, accountdomain.NormalizeEmail(*operator))
	if errors.Is(err, accountdomain.ErrNotFound) || (err == nil && (user.AdminRole == nil || *user.AdminRole != accountdomain.AdminSenior)) {
		log.WarnContext(ctx, "admin pd command refused", logx.KeyOp, op, "command", args[0], "reason", "not_senior")
		return errNotSenior
	}
	if err != nil {
		return err
	}
	in, err := newInfra(ctx, cfg, ModeAdmin, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	if args[0] == "export" {
		data, err := m.account.ExportSubject(ctx, *email, user.ID)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(subjectJSON(data))
	}
	res, err := m.account.AnonymizeSubject(ctx, *email, user.ID, *reason)
	if err != nil {
		return err
	}
	if !res.Changed {
		fmt.Fprintf(out, "user %s is already anonymized\n", res.UserID)
		return nil
	}
	fmt.Fprintf(out, "user %s anonymized: sessions cleared %d, login codes deleted %d, organization contacts cleared %t\n",
		res.UserID, res.SessionsCleared, res.CodesDeleted, res.OrganizationCleared)
	return nil
}

type subjectExport struct {
	ExportedAt   time.Time            `json:"exported_at"`
	User         subjectUser          `json:"user"`
	Organization *subjectOrganization `json:"organization,omitempty"`
	Acceptances  []subjectAcceptance  `json:"legal_acceptances"`
	Sessions     []subjectSession     `json:"sessions"`
}

type subjectUser struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	BlockedAt    *time.Time `json:"blocked_at,omitempty"`
	AnonymizedAt *time.Time `json:"anonymized_at,omitempty"`
}

type subjectOrganization struct {
	ID              string  `json:"id"`
	LegalType       string  `json:"legal_type"`
	INN             string  `json:"inn"`
	Name            string  `json:"name"`
	ContactName     string  `json:"contact_name"`
	Phone           string  `json:"phone"`
	Address         *string `json:"address,omitempty"`
	KPP             *string `json:"kpp,omitempty"`
	SignerName      *string `json:"signer_name,omitempty"`
	SignerPosition  *string `json:"signer_position,omitempty"`
	BloggerFullName *string `json:"blogger_full_name,omitempty"`
	BankAccount     *string `json:"bank_account,omitempty"`
	BankName        *string `json:"bank_name,omitempty"`
	BankBIK         *string `json:"bank_bik,omitempty"`
	TaxDocument     bool    `json:"tax_document_uploaded"`
	Members         int     `json:"members"`
}

type subjectAcceptance struct {
	Document   string    `json:"document"`
	Version    int       `json:"version"`
	AcceptedAt time.Time `json:"accepted_at"`
	IP         string    `json:"ip,omitempty"`
}

type subjectSession struct {
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	IP        string     `json:"ip,omitempty"`
	UserAgent *string    `json:"user_agent,omitempty"`
}

func ipString(ip *netip.Addr) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}

// subjectJSON — выгрузка для субъекта: его собственные данные без маскирования.
func subjectJSON(d *accountapp.SubjectData) subjectExport {
	u := d.User
	out := subjectExport{
		ExportedAt: time.Now().UTC(),
		User: subjectUser{ID: u.ID.String(), Email: u.Email, Status: string(u.Status), CreatedAt: u.CreatedAt,
			LastLoginAt: u.LastLoginAt, BlockedAt: u.BlockedAt, AnonymizedAt: u.AnonymizedAt},
		Acceptances: []subjectAcceptance{}, Sessions: []subjectSession{},
	}
	if o := d.Organization; o != nil {
		org := &subjectOrganization{
			ID: o.ID.String(), LegalType: string(o.LegalType), INN: o.INN, Name: o.Name, ContactName: o.ContactName,
			Phone: o.Phone, Address: o.Address, KPP: o.KPP, SignerName: o.SignerName, SignerPosition: o.SignerPosition,
			BankAccount: o.BankAccount, BankName: o.BankName, BankBIK: o.BankBIK, TaxDocument: o.TaxDocument != nil,
			Members: d.Members,
		}
		if o.Blogger != nil {
			org.BloggerFullName = o.Blogger.FullName
		}
		out.Organization = org
	}
	for _, a := range d.Acceptances {
		out.Acceptances = append(out.Acceptances, subjectAcceptance{Document: string(a.Kind), Version: a.Version,
			AcceptedAt: a.AcceptedAt, IP: ipString(a.IP)})
	}
	for _, s := range d.Sessions {
		out.Sessions = append(out.Sessions, subjectSession{CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt, RevokedAt: s.RevokedAt,
			IP: ipString(s.IP), UserAgent: s.UserAgent})
	}
	return out
}
