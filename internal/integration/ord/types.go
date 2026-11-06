package ord

import (
	"encoding/json"
	"strings"
)

// Organization — тело POST /organization.
type Organization struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	IsOrs bool   `json:"isOrs"`
	IsRr  bool   `json:"isRr"`
	INN   string `json:"inn,omitempty"`
	Name  string `json:"name,omitempty"`
	KPP   string `json:"kpp,omitempty"`
}

// Contract — тело POST /contract.
type Contract struct {
	ID                      string `json:"id"`
	Type                    string `json:"type"`
	ContractorID            string `json:"contractorId"`
	ClientID                string `json:"clientId"`
	IsRegReport             bool   `json:"isRegReport"`
	Date                    string `json:"date"`
	Number                  string `json:"number,omitempty"`
	SubjectType             string `json:"subjectType,omitempty"`
	ActionType              string `json:"actionType,omitempty"`
	AgentActingForPublisher *bool  `json:"agentActingForPublisher,omitempty"`
}

// Platforms — тело POST /platforms.
type Platforms struct {
	OrganizationID string     `json:"organizationId"`
	Platforms      []Platform `json:"platforms"`
}

// Platform — площадка.
type Platform struct {
	PlatformID string `json:"platformId"`
	IsOwned    bool   `json:"isOwned"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	URL        string `json:"url,omitempty"`
}

// Creative — тело POST /creative.
type Creative struct {
	ID            string      `json:"id"`
	CreativeType  string      `json:"creativeType"`
	IsSocial      bool        `json:"isSocial"`
	IsSocialQuota bool        `json:"isSocialQuota"`
	CoBranding    bool        `json:"coBranding"`
	ContractIDs   []string    `json:"contractIds"`
	KKTUCodes     []string    `json:"kktuCodes"`
	Description   string      `json:"description,omitempty"`
	Form          string      `json:"form"`
	TextData      []string    `json:"textData,omitempty"`
	MediaData     []MediaData `json:"mediaData,omitempty"`
	URLs          []string    `json:"urls,omitempty"`
}

// MediaData — файл креатива по публичной ссылке.
type MediaData struct {
	MediaURL         string `json:"mediaUrl"`
	MediaURLFileType string `json:"mediaUrlFileType"`
}

// Receipt — ответ ResponseStatus на POST и GET /status.
type Receipt struct {
	// ObjectID is returned by GET /organization; data is deliberately not decoded.
	ObjectID       string            `json:"object_id,omitempty"`
	RequestID      string            `json:"request_id"`
	Status         string            `json:"status"`
	Token          string            `json:"token"`
	ObjectStatuses []ObjectStatus    `json:"object_statuses"`
	ErrorMessage   []json.RawMessage `json:"error_message"`
}

// ObjectStatus — статус объекта запроса.
type ObjectStatus struct {
	ObjectID     string            `json:"object_id"`
	Status       string            `json:"status"`
	Token        string            `json:"token"`
	ErrorMessage []json.RawMessage `json:"error_message"`
}

// Errors — тексты error_message[]: строки как есть, прочие значения — JSON.
func (r Receipt) Errors() []string { return messages(r.ErrorMessage) }

// Errors — тексты error_message[] объекта.
func (o ObjectStatus) Errors() []string { return messages(o.ErrorMessage) }

func messages(raw []json.RawMessage) []string {
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		var s string
		if json.Unmarshal(m, &s) == nil {
			out = append(out, s)
			continue
		}
		out = append(out, strings.TrimSpace(string(m)))
	}
	return out
}

// Statistics — тело POST /statistics: показы и сумма месяца по паре «креатив + площадка»
// (этап «API администратора и отчётность ОРД», R-20).
// ПРЕДПОЛОЖЕНИЕ (Q-42): одна запись на вызов, поля и формат дат (YYYY-MM-DD) — по образцу других
// методов API v8; сумма — рубли строкой с точкой (amount.incl). Проверяется на тестовом контуре.
type Statistics struct {
	ID            string `json:"id"`
	CreativeID    string `json:"creativeId"`
	PlatformID    string `json:"platformId"`
	ImpsFact      int64  `json:"impsFact"`
	ImpsPlan      int64  `json:"impsPlan"`
	StartDateFact string `json:"startDateFact"`
	EndDateFact   string `json:"endDateFact"`
	StartDatePlan string `json:"startDatePlan"`
	EndDatePlan   string `json:"endDatePlan"`
	Amount        Amount `json:"amount"`
}

// Amount — сумма в рублях строкой.
type Amount struct {
	Incl string `json:"incl"`
}

// Invoice — тело POST /invoice: акт месяца по договору (R-22).
// ПРЕДПОЛОЖЕНИЕ (Q-42, Q-43): поля по образцу API v8; тип — из конфигурации
// (ORD_INVOICE_TYPE_INITIAL, ORD_INVOICE_TYPE_PUBLISHER); повтор с тем же id — обновление.
type Invoice struct {
	ID         string `json:"id"`
	ContractID string `json:"contractId"`
	Type       string `json:"type"`
	Number     string `json:"number"`
	Date       string `json:"date"`
	StartDate  string `json:"startDate"`
	EndDate    string `json:"endDate"`
	Amount     Amount `json:"amount"`
	IsVat      bool   `json:"isVat"`
}

// InvoiceCreatives — тело POST /invoice/creatives: разбивка акта по креативам и площадкам.
// ПРЕДПОЛОЖЕНИЕ (Q-42): второй вызов после /invoice; опрос — по reqid этого вызова.
type InvoiceCreatives struct {
	InvoiceID string        `json:"invoiceId"`
	Items     []InvoiceItem `json:"items"`
}

// InvoiceItem — строка разбивки акта.
type InvoiceItem struct {
	CreativeID string `json:"creativeId"`
	PlatformID string `json:"platformId"`
	Amount     Amount `json:"amount"`
}
