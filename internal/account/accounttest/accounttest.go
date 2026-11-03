// Package accounttest — тестовые данные модуля аккаунтов: корректные ИНН и
// типовые наборы реквизитов для S-01 и S-10.
package accounttest

import (
	"fmt"
	"sync/atomic"

	"github.com/leha-creator/chanly/internal/account/domain"
)

var seq atomic.Int64

// INN возвращает новый корректный ИНН для типа лица: 10 цифр у юрлица, 12 у остальных.
func INN(t domain.LegalType) string {
	n := seq.Add(1)
	prefix := fmt.Sprintf("77%06d", n%1_000_000)
	digits := 2
	if t != domain.LegalEntity {
		prefix = fmt.Sprintf("50%08d", n%100_000_000)
	} else {
		prefix += "0"
		digits = 1
	}
	limit := 10
	if digits == 2 {
		limit = 100
	}
	for tail := range limit {
		inn := prefix + fmt.Sprintf("%0*d", digits, tail)
		if domain.CheckINN(inn, t) == "" {
			return inn
		}
	}
	panic("accounttest: no valid INN for prefix " + prefix)
}

func s(v string) *string { return &v }

// AdvertiserLegalEntity — ООО-рекламодатель (S-01).
func AdvertiserLegalEntity() domain.NewOrganizationInput {
	return domain.NewOrganizationInput{
		Cabinet: domain.CabinetAdvertiser, LegalType: domain.LegalEntity, INN: INN(domain.LegalEntity),
		Fields: domain.Fields{Name: s("ООО «Ромашка»"), ContactName: s("Иван Тестов"), Phone: s("+79990000001"),
			Address: s("г. Москва, ул. Примерная, д. 1")},
	}
}

// BloggerSelfEmployed — блогер-самозанятый на НПД (S-10).
func BloggerSelfEmployed() domain.NewOrganizationInput {
	return domain.NewOrganizationInput{
		Cabinet: domain.CabinetBlogger, LegalType: domain.SelfEmployed, INN: INN(domain.SelfEmployed),
		Fields: domain.Fields{Name: s("Петров Пётр Петрович"), ContactName: s("Пётр Петров"), Phone: s("+79990000002")},
	}
}

// BloggerFields — поля блогера-юрлица для AddCabinet.
func BloggerFields() domain.Fields {
	osno, vat := domain.TaxOSNO, domain.Vat22
	return domain.Fields{
		KPP: s("770701001"), TaxRegime: &osno, VatRate: &vat,
		SignerPosition: s("Генеральный директор"), SignerName: s("Тестов Иван Иванович"),
		SignerBasisName: s("Устав"), SignerBasisNumber: s("1"),
	}
}
