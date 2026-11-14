package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// Причины отказа в полях организации (details.reason).
const (
	ReasonRequired      = "required"
	ReasonNotAllowed    = "not_allowed"
	ReasonInvalidFormat = "invalid_format"
	ReasonInvalidValue  = "invalid_value"
	ReasonImmutable     = "immutable"
	ReasonCabinetExists = "cabinet_already_open"
)

var (
	phoneRe = regexp.MustCompile(`^\+7[0-9]{10}$`)
	kppRe   = regexp.MustCompile(`^[0-9]{9}$`)
)

// FileRef — файл в закрытой области хранилища.
type FileRef struct {
	StorageKey string
	SHA256     string
}

// BloggerProfile — профиль блогера (04 §2). Наличие означает, что у организации есть роль блогера.
type BloggerProfile struct {
	FullName *string
}

// Organization — организация пользователя с реквизитами и ролями (04 §2).
type Organization struct {
	ID          uuid.UUID
	LegalType   LegalType
	INN         string
	Name        string
	ContactName string
	Phone       string
	Address     *string

	// Поля только блогера (D-24): у организации, которая только рекламодатель, пустые.
	KPP               *string
	TaxRegime         *TaxRegime
	VatRate           *VatRate
	TaxDocument       *FileRef
	SignerPosition    *string
	SignerName        *string
	SignerBasisName   *string
	SignerBasisNumber *string

	// Банковские реквизиты рекламодателя (D-26).
	BankAccount     *string
	BankName        *string
	BankCorrAccount *string
	BankBIK         *string

	Status     Status
	Advertiser bool
	Blogger    *BloggerProfile

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields — изменяемые реквизиты. nil — поле не передано.
type Fields struct {
	Name              *string
	ContactName       *string
	Phone             *string
	KPP               *string
	Address           *string
	TaxRegime         *TaxRegime
	VatRate           *VatRate
	SignerPosition    *string
	SignerName        *string
	SignerBasisName   *string
	SignerBasisNumber *string
	BankAccount       *string
	BankName          *string
	BankCorrAccount   *string
	BankBIK           *string
	BloggerFullName   *string
}

// NewOrganizationInput — данные для создания организации с первым кабинетом.
type NewOrganizationInput struct {
	Cabinet   Cabinet
	LegalType LegalType
	INN       string
	Fields    Fields
}

// NewOrganization создаёт организацию с одним кабинетом и проверяет реквизиты (S-01, S-10).
func NewOrganization(id uuid.UUID, in NewOrganizationInput, now time.Time) (*Organization, error) {
	if !in.LegalType.Valid() {
		return nil, apperr.Validation("legal_type", ReasonInvalidValue)
	}
	if !in.Cabinet.Valid() {
		return nil, apperr.Validation("cabinet", ReasonInvalidValue)
	}
	inn := strings.TrimSpace(in.INN)
	if reason := CheckINN(inn, in.LegalType); reason != "" {
		return nil, apperr.Validation("inn", reason)
	}
	o := &Organization{
		ID:        id,
		LegalType: in.LegalType,
		INN:       inn,
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := o.openCabinet(in.Cabinet, in.Fields); err != nil {
		return nil, err
	}
	return o, nil
}

// AddCabinet открывает второй кабинет у существующей организации («стать
// блогером», «стать рекламодателем»). Общие реквизиты не меняются; поля,
// которых не хватает роли блогера, берутся из f и обязательны.
func (o *Organization) AddCabinet(c Cabinet, f Fields, now time.Time) error {
	if !c.Valid() {
		return apperr.Validation("cabinet", ReasonInvalidValue)
	}
	if o.HasCabinet(c) {
		return apperr.Conflict("cabinet is already open").WithDetail("reason", ReasonCabinetExists)
	}
	if c == CabinetAdvertiser {
		f = Fields{} // рекламодателю дополнительных полей не нужно
	} else {
		// Блогеру нужны только его поля; общие реквизиты остаются прежними.
		f.Name, f.ContactName, f.Phone, f.Address = nil, nil, nil, nil
		f.BankAccount, f.BankName, f.BankCorrAccount, f.BankBIK = nil, nil, nil, nil
	}
	if err := o.openCabinet(c, f); err != nil {
		return err
	}
	o.UpdatedAt = now
	return nil
}

func (o *Organization) openCabinet(c Cabinet, f Fields) error {
	next := o.clone()
	if c == CabinetAdvertiser {
		if err := onlyBloggerFieldsAbsent(f); err != nil {
			return err
		}
		next.Advertiser = true
	} else {
		next.Blogger = &BloggerProfile{}
		// Самозанятому режим и ставка подставляются без выбора: НПД, без НДС (08 §4.3).
		if next.LegalType == SelfEmployed {
			if f.TaxRegime == nil {
				f.TaxRegime = ptr(TaxNPD)
			}
			if f.VatRate == nil {
				f.VatRate = ptr(VatNone)
			}
		}
	}
	next.apply(f)
	if next.Blogger != nil && next.Blogger.FullName == nil && next.LegalType != LegalEntity {
		// Форма блогера спрашивает ФИО один раз (08 §4.3): у ИП и самозанятого это наименование.
		name := next.Name
		next.Blogger.FullName = &name
	}
	if err := next.validate(); err != nil {
		return err
	}
	*o = *next
	return nil
}

// Update меняет реквизиты (PATCH). ИНН и тип лица не меняются. Возвращает имена
// изменённых полей — для журнала и аудита.
func (o *Organization) Update(f Fields, now time.Time) ([]string, error) {
	next := o.clone()
	if next.Blogger == nil {
		if err := onlyBloggerFieldsAbsent(f); err != nil {
			return nil, err
		}
	}
	next.apply(f)
	if err := next.validate(); err != nil {
		return nil, err
	}
	changed := ChangedFields(o.Values(), next.Values())
	if len(changed) == 0 {
		return nil, nil
	}
	next.UpdatedAt = now
	*o = *next
	return changed, nil
}

// SetTaxDocument прикрепляет документ о режиме налогообложения (только блогер).
func (o *Organization) SetTaxDocument(ref FileRef, now time.Time) error {
	if o.Blogger == nil {
		return apperr.Forbidden(ReasonCabinetRequired)
	}
	o.TaxDocument = &ref
	o.UpdatedAt = now
	return nil
}

// ReasonCabinetRequired — у организации нет нужного кабинета (details.reason).
const ReasonCabinetRequired = "cabinet_required"

// HasCabinet сообщает, что кабинет открыт.
func (o *Organization) HasCabinet(c Cabinet) bool {
	switch c {
	case CabinetAdvertiser:
		return o.Advertiser
	case CabinetBlogger:
		return o.Blogger != nil
	}
	return false
}

// Cabinets возвращает открытые кабинеты в постоянном порядке.
func (o *Organization) Cabinets() []Cabinet {
	var out []Cabinet
	if o.Advertiser {
		out = append(out, CabinetAdvertiser)
	}
	if o.Blogger != nil {
		out = append(out, CabinetBlogger)
	}
	return out
}

// Values — реквизиты как набор «поле → значение» для сравнения и аудита.
// Ключи совпадают с полями API.
func (o *Organization) Values() map[string]any {
	v := map[string]any{
		"legal_type":   string(o.LegalType),
		"inn":          o.INN,
		"name":         o.Name,
		"contact_name": o.ContactName,
		"phone":        o.Phone,
		"status":       string(o.Status),
		"cabinets":     cabinetNames(o.Cabinets()),
	}
	opt := func(key string, p *string) {
		if p != nil {
			v[key] = *p
		}
	}
	opt("address", o.Address)
	opt("kpp", o.KPP)
	if o.TaxRegime != nil {
		v["tax_regime"] = string(*o.TaxRegime)
	}
	if o.VatRate != nil {
		v["vat_rate"] = string(*o.VatRate)
	}
	if o.TaxDocument != nil {
		v["tax_document_sha256"] = o.TaxDocument.SHA256
	}
	opt("signer_position", o.SignerPosition)
	opt("signer_name", o.SignerName)
	opt("signer_basis_name", o.SignerBasisName)
	opt("signer_basis_number", o.SignerBasisNumber)
	opt("bank_account", o.BankAccount)
	opt("bank_name", o.BankName)
	opt("bank_corr_account", o.BankCorrAccount)
	opt("bank_bik", o.BankBIK)
	if o.Blogger != nil {
		opt("blogger_full_name", o.Blogger.FullName)
	}
	return v
}

// ChangedFields возвращает ключи, значения которых различаются.
func ChangedFields(before, after map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range []map[string]any{before, after} {
		for k := range m {
			if seen[k] {
				continue
			}
			seen[k] = true
			if !equalValue(before[k], after[k]) {
				out = append(out, k)
			}
		}
	}
	slices.Sort(out)
	return out
}

func equalValue(a, b any) bool {
	as, aok := a.([]string)
	bs, bok := b.([]string)
	if aok || bok {
		return strings.Join(as, ",") == strings.Join(bs, ",")
	}
	return a == b
}

func cabinetNames(cs []Cabinet) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

func (o *Organization) clone() *Organization {
	c := *o
	if o.Blogger != nil {
		b := *o.Blogger
		c.Blogger = &b
	}
	return &c
}

// apply переносит переданные поля; строки обрезаются, пустая необязательная строка — «нет значения».
func (o *Organization) apply(f Fields) {
	setReq := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	setOpt := func(dst **string, v *string) {
		if v == nil {
			return
		}
		s := strings.TrimSpace(*v)
		if s == "" {
			*dst = nil
			return
		}
		*dst = &s
	}
	setReq(&o.Name, f.Name)
	setReq(&o.ContactName, f.ContactName)
	setReq(&o.Phone, f.Phone)
	setOpt(&o.Address, f.Address)
	setOpt(&o.KPP, f.KPP)
	if f.TaxRegime != nil {
		o.TaxRegime = ptr(*f.TaxRegime)
	}
	if f.VatRate != nil {
		o.VatRate = ptr(*f.VatRate)
	}
	setOpt(&o.SignerPosition, f.SignerPosition)
	setOpt(&o.SignerName, f.SignerName)
	setOpt(&o.SignerBasisName, f.SignerBasisName)
	setOpt(&o.SignerBasisNumber, f.SignerBasisNumber)
	setOpt(&o.BankAccount, f.BankAccount)
	setOpt(&o.BankName, f.BankName)
	setOpt(&o.BankCorrAccount, f.BankCorrAccount)
	setOpt(&o.BankBIK, f.BankBIK)
	if o.Blogger != nil {
		setOpt(&o.Blogger.FullName, f.BloggerFullName)
	}
}

// onlyBloggerFieldsAbsent отклоняет поля блогера у организации без кабинета блогера (D-24).
func onlyBloggerFieldsAbsent(f Fields) error {
	present := []struct {
		name string
		set  bool
	}{
		{"kpp", f.KPP != nil},
		{"tax_regime", f.TaxRegime != nil},
		{"vat_rate", f.VatRate != nil},
		{"signer_position", f.SignerPosition != nil},
		{"signer_name", f.SignerName != nil},
		{"signer_basis_name", f.SignerBasisName != nil},
		{"signer_basis_number", f.SignerBasisNumber != nil},
		{"blogger_full_name", f.BloggerFullName != nil},
	}
	for _, p := range present {
		if p.set {
			return apperr.Validation(p.name, ReasonNotAllowed)
		}
	}
	return nil
}

// validate проверяет состояние целиком по правилам 04 §2 и D-24.
func (o *Organization) validate() error {
	if o.Name == "" {
		return apperr.Validation("name", ReasonRequired)
	}
	if o.ContactName == "" {
		return apperr.Validation("contact_name", ReasonRequired)
	}
	if !phoneRe.MatchString(o.Phone) {
		return apperr.Validation("phone", ReasonInvalidFormat)
	}
	switch o.LegalType {
	case LegalEntity, SoleProprietor:
		if o.Address == nil {
			return apperr.Validation("address", ReasonRequired)
		}
	case SelfEmployed:
		if o.Address != nil {
			return apperr.Validation("address", ReasonNotAllowed)
		}
	}
	if o.Blogger == nil {
		return nil
	}
	return o.validateBlogger()
}

func (o *Organization) validateBlogger() error {
	if o.TaxRegime == nil {
		return apperr.Validation("tax_regime", ReasonRequired)
	}
	if !TaxRegimeAllowed(o.LegalType, *o.TaxRegime) {
		return apperr.Validation("tax_regime", ReasonInvalidValue)
	}
	if o.VatRate == nil {
		return apperr.Validation("vat_rate", ReasonRequired)
	}
	if !VatRateAllowed(*o.TaxRegime, *o.VatRate) {
		return apperr.Validation("vat_rate", ReasonInvalidValue)
	}
	signer := []struct {
		name string
		v    *string
	}{
		{"kpp", o.KPP},
		{"signer_position", o.SignerPosition},
		{"signer_name", o.SignerName},
		{"signer_basis_name", o.SignerBasisName},
		{"signer_basis_number", o.SignerBasisNumber},
	}
	for _, s := range signer {
		optional := s.name == "signer_basis_name" || s.name == "signer_basis_number"
		if o.LegalType == LegalEntity && s.v == nil && !optional {
			return apperr.Validation(s.name, ReasonRequired)
		}
		if o.LegalType != LegalEntity && s.v != nil {
			return apperr.Validation(s.name, ReasonNotAllowed)
		}
	}
	if o.KPP != nil && !kppRe.MatchString(*o.KPP) {
		return apperr.Validation("kpp", ReasonInvalidFormat)
	}
	full := o.Blogger.FullName
	if o.LegalType == LegalEntity && full != nil {
		return apperr.Validation("blogger_full_name", ReasonNotAllowed)
	}
	if o.LegalType != LegalEntity && full == nil {
		return apperr.Validation("blogger_full_name", ReasonRequired)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
