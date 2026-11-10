package rocketwork

import (
	"encoding/json"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// RedactFields — поля, скрываемые outbound в сохранённых запросах и ответах Рокет Ворк (R-18):
// ПДн исполнителя и ссылки онбординга и чека (они для блогера, не для журнала).
var RedactFields = []string{"inn", "phone", "full_name", "onboarding_url", "receipt_url",
	"task", "tasks", "executor", "executors", "requisites", "company_inn", "company_name",
	"errors", "message", "receipt_uri", "commission", "authorization_credentials"}

// recordBody — тело запроса для outbound_request (06 §8, 07 §5.4): ИНН, телефон и ФИО
// маскированы, остальные поля нужны для разбора ошибок.
func recordBody(body any) []byte {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return redactJSON(raw)
}

// redactJSON маскирует персональные поля JSON-объекта; не объект — как есть.
func redactJSON(raw []byte) []byte {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	if v, ok := m["inn"].(string); ok {
		m["inn"] = logx.MaskINN(v)
	}
	if v, ok := m["phone"].(string); ok {
		m["phone"] = logx.MaskPhone(v)
	}
	if v, ok := m["full_name"].(string); ok {
		m["full_name"] = logx.MaskName(v)
	}
	// Ссылки онбординга и чека — данные блогера, в журнал не идут (R-18).
	for _, k := range []string{"onboarding_url", "receipt_url"} {
		if _, ok := m[k]; ok {
			m[k] = "[redacted]"
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}
