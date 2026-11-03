package ord

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/leha-creator/chanly/internal/platform/logx"
)

// recordBody — тело запроса для outbound_request (R-16): ИНН маскирован, вместо текста
// креатива — его длина. Остальные поля нужны администратору для разбора ошибок (S-20).
func recordBody(body any) []byte {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	if inn, ok := m["inn"].(string); ok {
		m["inn"] = logx.MaskINN(inn)
	}
	if texts, ok := m["textData"].([]any); ok {
		lengths := make([]map[string]int, 0, len(texts))
		for _, t := range texts {
			s, _ := t.(string)
			lengths = append(lengths, map[string]int{"length": utf8.RuneCountInString(s)})
		}
		m["textData"] = lengths
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}
