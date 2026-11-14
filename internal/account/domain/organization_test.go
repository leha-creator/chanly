package domain

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

var now = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

// innWithChecksum достраивает контрольные цифры к префиксу из 9 или 10 цифр.
func innWithChecksum(prefix string) string {
	d := make([]int, len(prefix))
	for i := range prefix {
		d[i] = int(prefix[i] - '0')
	}
	if len(prefix) == 9 {
		return prefix + string(rune('0'+innCheckDigit(d, innWeights10)))
	}
	d = append(d, innCheckDigit(d, innWeights11))
	d = append(d, innCheckDigit(d, innWeights12))
	return prefix + string(rune('0'+d[10])) + string(rune('0'+d[11]))
}

func TestCheckINN(t *testing.T) {
	cases := []struct {
		inn  string
		lt   LegalType
		want string
	}{
		{"7707083893", LegalEntity, ""},
		{"500100732259", SoleProprietor, ""},
		{"500100732259", SelfEmployed, ""},
		{innWithChecksum("772800000"), LegalEntity, ""},
		{innWithChecksum("7728000001"), SelfEmployed, ""},
		{"7707083894", LegalEntity, ReasonInvalidChecksum},
		{"500100732258", SoleProprietor, ReasonInvalidChecksum},
		{"500100732249", SoleProprietor, ReasonInvalidChecksum}, // неверна 11-я цифра
		{"7707083893", SoleProprietor, ReasonInvalidLength},
		{"500100732259", LegalEntity, ReasonInvalidLength},
		{"77070838a3", LegalEntity, ReasonInvalidLength},
		{"", LegalEntity, ReasonInvalidLength},
	}
	for _, c := range cases {
		if got := CheckINN(c.inn, c.lt); got != c.want {
			t.Errorf("CheckINN(%q, %s) = %q, want %q", c.inn, c.lt, got, c.want)
		}
	}
}

func TestTaxTables(t *testing.T) {
	// Каждая строка таблиц 04 §2 (Q-37).
	regimes := map[LegalType][]TaxRegime{
		SelfEmployed:   {TaxNPD},
		SoleProprietor: {TaxOSNO, TaxUSN, TaxAUSN, TaxESHN, TaxPSN, TaxNPD},
		LegalEntity:    {TaxOSNO, TaxUSN, TaxAUSN, TaxESHN},
	}
	all := []TaxRegime{TaxOSNO, TaxUSN, TaxAUSN, TaxESHN, TaxPSN, TaxNPD}
	for lt, allowed := range regimes {
		for _, r := range all {
			if got := TaxRegimeAllowed(lt, r); got != slices.Contains(allowed, r) {
				t.Errorf("TaxRegimeAllowed(%s, %s) = %v", lt, r, got)
			}
		}
	}
	rates := map[TaxRegime][]VatRate{
		TaxOSNO: {Vat22, VatNone},
		TaxUSN:  {VatNone, Vat5, Vat7, Vat22},
		TaxESHN: {Vat22, VatNone},
		TaxAUSN: {VatNone},
		TaxPSN:  {VatNone},
		TaxNPD:  {VatNone},
	}
	for r, allowed := range rates {
		for _, v := range []VatRate{VatNone, Vat5, Vat7, Vat22} {
			if got := VatRateAllowed(r, v); got != slices.Contains(allowed, v) {
				t.Errorf("VatRateAllowed(%s, %s) = %v", r, v, got)
			}
		}
	}
}

func s(v string) *string { return &v }

func advertiserLE() NewOrganizationInput {
	return NewOrganizationInput{
		Cabinet: CabinetAdvertiser, LegalType: LegalEntity, INN: "7707083893",
		Fields: Fields{Name: s("ООО «Ромашка»"), ContactName: s("Иван Тестов"), Phone: s("+79990000001"),
			Address: s("г. Москва, ул. Примерная, д. 1")},
	}
}

func bloggerSE() NewOrganizationInput {
	return NewOrganizationInput{
		Cabinet: CabinetBlogger, LegalType: SelfEmployed, INN: "500100732259",
		Fields: Fields{Name: s("Петров Пётр Петрович"), ContactName: s("Пётр"), Phone: s("+79990000002")},
	}
}

func bloggerLE() NewOrganizationInput {
	in := advertiserLE()
	in.Cabinet = CabinetBlogger
	in.Fields.KPP = s("770701001")
	in.Fields.TaxRegime = ptr(TaxOSNO)
	in.Fields.VatRate = ptr(Vat22)
	in.Fields.SignerPosition = s("Генеральный директор")
	in.Fields.SignerName = s("Тестов Иван Иванович")
	in.Fields.SignerBasisName = s("Устав")
	in.Fields.SignerBasisNumber = s("1")
	return in
}

func wantValidation(t *testing.T, err error, field, reason string) {
	t.Helper()
	e, ok := apperr.As(err)
	if !ok || e.Code != apperr.CodeValidationFailed || e.Details["field"] != field || e.Details["reason"] != reason {
		t.Fatalf("want validation %s/%s, got %v", field, reason, err)
	}
}

func TestNewOrganizationValid(t *testing.T) {
	o, err := NewOrganization(uuid.New(), advertiserLE(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Advertiser || o.Blogger != nil || o.Status != StatusActive || o.KPP != nil {
		t.Fatalf("unexpected organization: %+v", o)
	}

	b, err := NewOrganization(uuid.New(), bloggerSE(), now)
	if err != nil {
		t.Fatal(err)
	}
	// Самозанятому режим и ставка подставляются, ФИО блогера — из наименования.
	if *b.TaxRegime != TaxNPD || *b.VatRate != VatNone || *b.Blogger.FullName != "Петров Пётр Петрович" {
		t.Fatalf("self-employed defaults: %+v %+v", b, b.Blogger)
	}

	if _, err := NewOrganization(uuid.New(), bloggerLE(), now); err != nil {
		t.Fatalf("legal entity blogger: %v", err)
	}
}

func TestNewOrganizationRejects(t *testing.T) {
	cases := []struct {
		name   string
		input  func() NewOrganizationInput
		field  string
		reason string
	}{
		{"bad inn checksum", func() NewOrganizationInput { in := advertiserLE(); in.INN = "7707083894"; return in }, "inn", ReasonInvalidChecksum},
		{"inn length for type", func() NewOrganizationInput { in := advertiserLE(); in.INN = "500100732259"; return in }, "inn", ReasonInvalidLength},
		{"unknown legal type", func() NewOrganizationInput { in := advertiserLE(); in.LegalType = "person"; return in }, "legal_type", ReasonInvalidValue},
		{"advertiser with tax regime", func() NewOrganizationInput { in := advertiserLE(); in.Fields.TaxRegime = ptr(TaxUSN); return in }, "tax_regime", ReasonNotAllowed},
		{"advertiser with kpp", func() NewOrganizationInput { in := advertiserLE(); in.Fields.KPP = s("770701001"); return in }, "kpp", ReasonNotAllowed},
		{"legal entity without address", func() NewOrganizationInput { in := advertiserLE(); in.Fields.Address = nil; return in }, "address", ReasonRequired},
		{"blank address", func() NewOrganizationInput { in := advertiserLE(); in.Fields.Address = s("  "); return in }, "address", ReasonRequired},
		{"self-employed with address", func() NewOrganizationInput { in := bloggerSE(); in.Fields.Address = s("Москва"); return in }, "address", ReasonNotAllowed},
		{"bad phone", func() NewOrganizationInput { in := advertiserLE(); in.Fields.Phone = s("89990000001"); return in }, "phone", ReasonInvalidFormat},
		{"missing name", func() NewOrganizationInput { in := advertiserLE(); in.Fields.Name = nil; return in }, "name", ReasonRequired},
		{"blogger without regime", func() NewOrganizationInput { in := bloggerLE(); in.Fields.TaxRegime = nil; return in }, "tax_regime", ReasonRequired},
		{"regime not for legal entity", func() NewOrganizationInput { in := bloggerLE(); in.Fields.TaxRegime = ptr(TaxPSN); return in }, "tax_regime", ReasonInvalidValue},
		{"self-employed on usn", func() NewOrganizationInput { in := bloggerSE(); in.Fields.TaxRegime = ptr(TaxUSN); return in }, "tax_regime", ReasonInvalidValue},
		{"vat not for regime", func() NewOrganizationInput { in := bloggerLE(); in.Fields.VatRate = ptr(Vat5); return in }, "vat_rate", ReasonInvalidValue},
		{"legal entity blogger without kpp", func() NewOrganizationInput { in := bloggerLE(); in.Fields.KPP = nil; return in }, "kpp", ReasonRequired},
		{"legal entity blogger without signer", func() NewOrganizationInput { in := bloggerLE(); in.Fields.SignerName = nil; return in }, "signer_name", ReasonRequired},
		{"self-employed with signer", func() NewOrganizationInput { in := bloggerSE(); in.Fields.SignerName = s("Кто-то"); return in }, "signer_name", ReasonNotAllowed},
		{"legal entity with blogger full name", func() NewOrganizationInput {
			in := bloggerLE()
			in.Fields.BloggerFullName = s("Иванов")
			return in
		}, "blogger_full_name", ReasonNotAllowed},
		{"bad kpp", func() NewOrganizationInput { in := bloggerLE(); in.Fields.KPP = s("12"); return in }, "kpp", ReasonInvalidFormat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewOrganization(uuid.New(), c.input(), now)
			wantValidation(t, err, c.field, c.reason)
		})
	}
}

func TestSoleProprietorBloggerCombinations(t *testing.T) {
	in := NewOrganizationInput{
		Cabinet: CabinetBlogger, LegalType: SoleProprietor, INN: "500100732259",
		Fields: Fields{Name: s("ИП Сидоров"), ContactName: s("Сидор"), Phone: s("+79990000003"),
			Address: s("Москва"), TaxRegime: ptr(TaxOSNO), VatRate: ptr(Vat5)},
	}
	_, err := NewOrganization(uuid.New(), in, now)
	wantValidation(t, err, "vat_rate", ReasonInvalidValue)

	in.Fields.TaxRegime, in.Fields.VatRate = ptr(TaxUSN), ptr(Vat5)
	if _, err := NewOrganization(uuid.New(), in, now); err != nil {
		t.Fatalf("usn/vat_5: %v", err)
	}
}

func TestAddCabinet(t *testing.T) {
	o, err := NewOrganization(uuid.New(), advertiserLE(), now)
	if err != nil {
		t.Fatal(err)
	}
	before := o.Values()

	// Блогеру-юрлицу не хватает его полей.
	err = o.AddCabinet(CabinetBlogger, Fields{TaxRegime: ptr(TaxUSN), VatRate: ptr(VatNone)}, now)
	wantValidation(t, err, "kpp", ReasonRequired)
	if o.Blogger != nil {
		t.Fatal("failed AddCabinet changed the organization")
	}

	later := now.Add(time.Hour)
	f := bloggerLE().Fields
	f.Name = s("Другое имя") // общие реквизиты при добавлении кабинета не меняются
	if err := o.AddCabinet(CabinetBlogger, f, later); err != nil {
		t.Fatal(err)
	}
	if o.Name != before["name"] || !o.UpdatedAt.Equal(later) {
		t.Fatalf("common fields changed or UpdatedAt not set: %q %v", o.Name, o.UpdatedAt)
	}
	if got := o.Cabinets(); !slices.Equal(got, []Cabinet{CabinetAdvertiser, CabinetBlogger}) {
		t.Fatalf("cabinets = %v", got)
	}

	err = o.AddCabinet(CabinetBlogger, f, later)
	if !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("second blogger cabinet: %v", err)
	}
}

func TestBloggerAddsAdvertiserCabinet(t *testing.T) {
	o, err := NewOrganization(uuid.New(), bloggerSE(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AddCabinet(CabinetAdvertiser, Fields{KPP: s("ignored")}, now); err != nil {
		t.Fatal(err)
	}
	if !o.Advertiser || o.KPP != nil {
		t.Fatalf("advertiser cabinet: %+v", o)
	}
}

func TestUpdate(t *testing.T) {
	o, err := NewOrganization(uuid.New(), advertiserLE(), now)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := o.Update(Fields{Phone: s("+79990000009"), BankBIK: s("044525000")}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(changed, []string{"bank_bik", "phone"}) {
		t.Fatalf("changed = %v", changed)
	}
	if changed, err := o.Update(Fields{Phone: s("+79990000009")}, now); err != nil || changed != nil {
		t.Fatalf("no-op update: %v %v", changed, err)
	}
	_, err = o.Update(Fields{TaxRegime: ptr(TaxUSN)}, now)
	wantValidation(t, err, "tax_regime", ReasonNotAllowed)

	b, _ := NewOrganization(uuid.New(), bloggerLE(), now)
	// Смена режима без ставки: прежняя ставка не подходит новому режиму.
	_, err = b.Update(Fields{TaxRegime: ptr(TaxAUSN)}, now)
	wantValidation(t, err, "vat_rate", ReasonInvalidValue)
	if *b.TaxRegime != TaxOSNO {
		t.Fatal("rejected update changed the organization")
	}
	if _, err := b.Update(Fields{TaxRegime: ptr(TaxAUSN), VatRate: ptr(VatNone)}, now); err != nil {
		t.Fatal(err)
	}
}

func TestSetTaxDocument(t *testing.T) {
	o, _ := NewOrganization(uuid.New(), advertiserLE(), now)
	err := o.SetTaxDocument(FileRef{StorageKey: "k", SHA256: "h"}, now)
	if e, ok := apperr.As(err); !ok || e.Code != apperr.CodeForbidden {
		t.Fatalf("advertiser tax document: %v", err)
	}
	b, _ := NewOrganization(uuid.New(), bloggerSE(), now)
	if err := b.SetTaxDocument(FileRef{StorageKey: "k", SHA256: "h"}, now); err != nil || b.TaxDocument == nil {
		t.Fatalf("blogger tax document: %v", err)
	}
}

func TestSignerBasisOptional(t *testing.T) {
	for _, basis := range []Fields{{}, {SignerBasisName: s("Устав")}, {SignerBasisNumber: s("1")}} {
		in := bloggerLE()
		in.Fields.SignerBasisName, in.Fields.SignerBasisNumber = basis.SignerBasisName, basis.SignerBasisNumber
		o, err := NewOrganization(uuid.New(), in, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.Update(Fields{Phone: s("+79990000009")}, now); err != nil {
			t.Fatal(err)
		}
		if (o.SignerBasisName == nil) != (basis.SignerBasisName == nil) || (o.SignerBasisNumber == nil) != (basis.SignerBasisNumber == nil) {
			t.Fatal("omitted PATCH changed basis")
		}
		if o.SignerBasisName != nil && *o.SignerBasisName != *basis.SignerBasisName || o.SignerBasisNumber != nil && *o.SignerBasisNumber != *basis.SignerBasisNumber {
			t.Fatal("omitted PATCH changed basis value")
		}
		if _, err := o.Update(Fields{SignerBasisName: s("  "), SignerBasisNumber: s("")}, now); err != nil {
			t.Fatal(err)
		}
		if o.SignerBasisName != nil || o.SignerBasisNumber != nil {
			t.Fatal("basis was not cleared")
		}
	}
	in := bloggerLE()
	in.Fields.SignerBasisName, in.Fields.SignerBasisNumber = nil, nil
	o, err := NewOrganization(uuid.New(), advertiserLE(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AddCabinet(CabinetBlogger, in.Fields, now); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"signer_basis_name", "signer_basis_number"} {
		in := bloggerSE()
		if field == "signer_basis_name" {
			in.Fields.SignerBasisName = s("Устав")
		} else {
			in.Fields.SignerBasisNumber = s("1")
		}
		_, err := NewOrganization(uuid.New(), in, now)
		wantValidation(t, err, field, ReasonNotAllowed)
	}
}

func TestRequiredDocuments(t *testing.T) {
	cases := []struct {
		cabinets []Cabinet
		want     []LegalDocumentKind
	}{
		{nil, nil},
		{[]Cabinet{CabinetAdvertiser}, []LegalDocumentKind{DocAdvertiserOffer, DocPlatformRules, DocPrivacyPolicy}},
		{[]Cabinet{CabinetBlogger}, []LegalDocumentKind{DocBloggerOffer, DocPlatformRules, DocPrivacyPolicy}},
		{[]Cabinet{CabinetAdvertiser, CabinetBlogger}, LegalDocumentKinds},
	}
	for _, c := range cases {
		if got := RequiredDocuments(c.cabinets); !slices.Equal(got, c.want) {
			t.Errorf("RequiredDocuments(%v) = %v, want %v", c.cabinets, got, c.want)
		}
	}
}

func TestPendingDocuments(t *testing.T) {
	adv1 := LegalDocument{ID: uuid.New(), Kind: DocAdvertiserOffer, Version: 2}
	blg := LegalDocument{ID: uuid.New(), Kind: DocBloggerOffer, Version: 1}
	rules := LegalDocument{ID: uuid.New(), Kind: DocPlatformRules, Version: 1}
	got := PendingDocuments([]LegalDocument{adv1, blg, rules}, []Cabinet{CabinetAdvertiser}, map[uuid.UUID]bool{rules.ID: true})
	if len(got) != 1 || got[0].ID != adv1.ID {
		t.Fatalf("pending = %+v", got)
	}
}

func TestLoginCode(t *testing.T) {
	pepper := []byte("pepper-pepper-pepper-pepper-pepp")
	code, err := GenerateCode()
	if err != nil || len(code) != CodeLength {
		t.Fatalf("GenerateCode = %q, %v", code, err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	newCode := func() *LoginCode {
		return NewLoginCode(uuid.New(), "User@Example.ru", code, pepper, nil, 10*time.Minute, now)
	}

	c := newCode()
	if c.Email != "user@example.ru" || c.CodeHash == code || c.CodeHash != HashCode(pepper, "user@example.ru", code) {
		t.Fatalf("code record: %+v", c)
	}
	if HashCode(pepper, "other@example.ru", code) == c.CodeHash || HashCode([]byte("other"), c.Email, code) == c.CodeHash {
		t.Fatal("hash does not depend on email or pepper")
	}
	if err := c.Verify(pepper, code, 5, now.Add(time.Minute)); err != nil || c.ConsumedAt == nil {
		t.Fatalf("valid code: %v", err)
	}
	if err := c.Verify(pepper, code, 5, now.Add(time.Minute)); !errors.Is(err, FailureNoCode) {
		t.Fatalf("reused code: %v", err)
	}

	c = newCode()
	if err := c.Verify(pepper, code, 5, now.Add(10*time.Minute)); !errors.Is(err, FailureExpired) {
		t.Fatalf("expired code: %v", err)
	}

	c = newCode()
	for i := 1; i <= 5; i++ {
		if err := c.Verify(pepper, wrong, 5, now); !errors.Is(err, FailureWrongCode) || c.Attempts != i {
			t.Fatalf("attempt %d: %v attempts=%d", i, err, c.Attempts)
		}
	}
	if err := c.Verify(pepper, code, 5, now); !errors.Is(err, FailureAttemptsExceeded) || c.ConsumedAt != nil {
		t.Fatalf("after 5 wrong attempts: %v", err)
	}
}

func TestSession(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil || len(token) < 43 || HashToken(token) != hash || hash == token {
		t.Fatalf("token %q hash %q err %v", token, hash, err)
	}
	ttl := 30 * 24 * time.Hour
	s := NewSession(uuid.New(), uuid.New(), hash, nil, nil, ttl, now)
	if !s.Active(now.Add(ttl-time.Second)) || s.Active(now.Add(ttl)) {
		t.Fatal("expiry boundary")
	}
	if s.Touch(ttl, now.Add(30*time.Minute)) {
		t.Fatal("touched within an hour")
	}
	later := now.Add(2 * time.Hour)
	if !s.Touch(ttl, later) || !s.ExpiresAt.Equal(later.Add(ttl)) {
		t.Fatalf("touch: %v", s.ExpiresAt)
	}
	revoked := now
	s.RevokedAt = &revoked
	if s.Active(later) {
		t.Fatal("revoked session is active")
	}

	admin := &User{AdminRole: ptr(AdminSenior)}
	if SessionTTL(admin, ttl, 12*time.Hour) != 12*time.Hour || SessionTTL(&User{}, ttl, 12*time.Hour) != ttl {
		t.Fatal("session ttl by role")
	}
}

func TestLegalDocumentKind(t *testing.T) {
	for _, k := range LegalDocumentKinds {
		if !k.Valid() || k.Title() == string(k) {
			t.Errorf("kind %s: valid=%v title=%q", k, k.Valid(), k.Title())
		}
	}
	if LegalDocumentKind("other").Valid() {
		t.Fatal("unknown kind is valid")
	}
}
