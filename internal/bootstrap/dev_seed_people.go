package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	accountapp "github.com/leha-creator/chanly/internal/account/app"
	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Учётки dev seed — те же, что у mockapi: фронтенд и тестировщики входят одними почтами.
const (
	seedAdvEmail       = "adv@example.test"
	seedBlgEmail       = "blg@example.test"
	seedBlgULEmail     = "blg-ul@example.test"
	seedAdminEmail     = "admin@example.test"
	seedModeratorEmail = "moderator@example.test"
)

// Баланс рекламодателя: ниже seedBalanceLow досыпается до seedBalance (R-6). Порог, а не
// точная сумма: заказы прошлого запуска держат часть баланса, и повтор ничего не пополняет.
const (
	seedBalance    money.Kopecks = 500_000_00
	seedBalanceLow money.Kopecks = 100_000_00
)

func sp(v string) *string { return &v }

// seedLegal публикует сгенерированный PDF для каждого вида документа без действующей версии.
func (s *seeder) seedLegal(ctx context.Context) error {
	current, err := s.m.account.CurrentLegalDocuments(ctx)
	if err != nil {
		return err
	}
	have := map[accountdomain.LegalDocumentKind]bool{}
	for _, d := range current {
		have[d.Kind] = true
	}
	for _, kind := range []accountdomain.LegalDocumentKind{accountdomain.DocAdvertiserOffer, accountdomain.DocBloggerOffer,
		accountdomain.DocPlatformRules, accountdomain.DocPrivacyPolicy} {
		if have[kind] {
			continue
		}
		doc, err := s.m.account.PublishLegalDocument(ctx, kind, seedPDF("maxim dev seed: "+string(kind)), s.m.in.clock.Now())
		if err != nil {
			return err
		}
		s.note("документ %s версии %d", kind, doc.Version)
	}
	return nil
}

// seedPeople — администраторы, рекламодатель и два блогера с принятыми документами.
func (s *seeder) seedPeople(ctx context.Context) error {
	var err error
	if s.admin, err = s.adminUser(ctx, seedAdminEmail, accountdomain.AdminSenior); err != nil {
		return err
	}
	if s.moderator, err = s.adminUser(ctx, seedModeratorEmail, accountdomain.AdminModerator); err != nil {
		return err
	}
	s.adv = seedOrg{email: seedAdvEmail}
	if err := s.organization(ctx, &s.adv, accountdomain.NewOrganizationInput{
		Cabinet: accountdomain.CabinetAdvertiser, LegalType: accountdomain.LegalEntity, INN: seedINN("770900001", accountdomain.LegalEntity),
		Fields: accountdomain.Fields{Name: sp("ООО «Демо Реклама»"), ContactName: sp("Анна Рекламова"), Phone: sp("+79990000101"),
			Address: sp("г. Москва, ул. Демонстрационная, д. 1")},
	}); err != nil {
		return err
	}
	npd, none := accountdomain.TaxNPD, accountdomain.VatNone
	s.blg = seedOrg{email: seedBlgEmail}
	if err := s.organization(ctx, &s.blg, accountdomain.NewOrganizationInput{
		Cabinet: accountdomain.CabinetBlogger, LegalType: accountdomain.SelfEmployed, INN: seedINN("5009000001", accountdomain.SelfEmployed),
		Fields: accountdomain.Fields{Name: sp("Блогерова Мария Сергеевна"), ContactName: sp("Мария Блогерова"),
			Phone: sp("+79990000102"), TaxRegime: &npd, VatRate: &none},
	}); err != nil {
		return err
	}
	osno, vat := accountdomain.TaxOSNO, accountdomain.Vat22
	s.blgUL = seedOrg{email: seedBlgULEmail}
	if err := s.organization(ctx, &s.blgUL, accountdomain.NewOrganizationInput{
		Cabinet: accountdomain.CabinetBlogger, LegalType: accountdomain.LegalEntity, INN: seedINN("770900002", accountdomain.LegalEntity),
		Fields: accountdomain.Fields{Name: sp("ООО «Демо Медиа»"), ContactName: sp("Олег Медиаев"), Phone: sp("+79990000103"),
			Address: sp("г. Москва, ул. Демонстрационная, д. 2"), KPP: sp("770901001"), TaxRegime: &osno, VatRate: &vat,
			SignerPosition: sp("Генеральный директор"), SignerName: sp("Медиаев Олег Петрович"),
			SignerBasisName: sp("Устав"), SignerBasisNumber: sp("1")},
	}); err != nil {
		return err
	}
	if s.rw == nil {
		s.skip(ctx, "rocketwork contractor "+seedBlgEmail, skipRocketWorkNoFake)
		return nil
	}
	return s.activeContractor(ctx, &s.blg)
}

func (s *seeder) adminUser(ctx context.Context, email string, role accountdomain.AdminRole) (uuid.UUID, error) {
	res, err := s.m.account.SetAdminRole(ctx, email, &role)
	if err != nil {
		return uuid.Nil, err
	}
	if res.Created || res.Changed {
		s.note("администратор %s (%s)", email, role)
	}
	return res.UserID, nil
}

// organization находит или создаёт пользователя и организацию и принимает документы кабинетов.
func (s *seeder) organization(ctx context.Context, o *seedOrg, in accountdomain.NewOrganizationInput) error {
	const op = "Bootstrap.DevSeed"
	u, created, err := s.m.account.DevEnsureUser(ctx, o.email)
	if err != nil {
		return err
	}
	o.userID = u.ID
	if created {
		s.log.InfoContext(ctx, "seed user", logx.KeyOp, op, logx.KeyUserID, u.ID, "email", logx.MaskEmail(o.email))
	}
	p := accountapp.Principal{UserID: u.ID}
	org, err := s.m.account.Organizations.GetByUser(ctx, u.ID)
	switch {
	case errors.Is(err, accountdomain.ErrNotFound):
		if org, err = s.m.account.CreateOrganization(ctx, p, in); err != nil {
			return fmt.Errorf("organization of %s: %w", logx.MaskEmail(o.email), err)
		}
		s.log.InfoContext(ctx, "seed organization", logx.KeyOp, op, "organization_id", org.ID, "cabinet", in.Cabinet,
			"legal_type", in.LegalType, "inn", logx.MaskINN(org.INN))
		s.note("организация %s (%s, %s)", org.Name, o.email, in.Cabinet)
	case err != nil:
		return err
	}
	o.orgID = org.ID
	me, err := s.m.account.GetMe(ctx, p)
	if err != nil {
		return err
	}
	if len(me.PendingDocuments) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(me.PendingDocuments))
	for _, d := range me.PendingDocuments {
		ids = append(ids, d.ID)
	}
	return s.m.account.AcceptLegalDocuments(ctx, p, ids, nil)
}

// activeContractor проводит онбординг самозанятого в Рокет Ворк до active (R-5).
func (s *seeder) activeContractor(ctx context.Context, o *seedOrg) error {
	c, err := s.m.payoutStore.ContractorByOrg(ctx, o.orgID, false)
	if err == nil && c.Status == payoutdomain.ContractorActive {
		return nil
	}
	if _, err := s.m.payout.Invite(ctx, o.orgID); err != nil {
		return fmt.Errorf("rocketwork invite: %w", err)
	}
	if err := s.m.payout.EnsureContractor(ctx, o.orgID); err != nil {
		return fmt.Errorf("rocketwork ensure contractor: %w", err)
	}
	if c, err = s.m.payoutStore.ContractorByOrg(ctx, o.orgID, false); err != nil {
		return err
	}
	if err := s.rw.ActivateContractor(c.ExternalID); err != nil {
		return err
	}
	if err := s.m.payout.ApplyContractor(ctx, c.ExternalID); err != nil {
		return err
	}
	s.note("исполнитель Рокет Ворк %s активен", o.email)
	return nil
}

// seedMoney досыпает баланс рекламодателя счётом и выпиской — тем же путём, что dev balance deposit.
func (s *seeder) seedMoney(ctx context.Context) error {
	available, _, err := s.m.ledger.Balance(ctx, s.adv.orgID)
	if err != nil {
		return err
	}
	if available >= seedBalanceLow {
		return nil
	}
	amount := seedBalance - available
	inv, err := s.m.billing.CreateInvoice(ctx, s.adv.orgID, s.adv.userID, amount, nil)
	if err != nil {
		return err
	}
	file := devStatementFile(s.cfg.BillingSupplier().BankAccount, s.m.in.clock.Now(), nil, devPayment{
		number: strings.TrimPrefix(inv.Number, "MX-"), amount: amount, inn: inv.Payer.INN,
		purpose: "Пополнение баланса по счёту № " + inv.Number,
	})
	if _, err := s.m.billing.ImportStatement(ctx, s.admin, file); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "seed balance", logx.KeyOp, "Bootstrap.DevSeed", "organization_id", s.adv.orgID,
		"invoice", inv.Number, "amount", int64(amount))
	s.note("баланс %s пополнен на %s (счёт %s)", seedAdvEmail, rub(amount), inv.Number)
	return nil
}
