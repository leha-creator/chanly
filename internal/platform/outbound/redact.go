package outbound

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// maxStoredBody — предел тела ответа в outbound_request.
const maxStoredBody = 64 << 10

const redacted = "[REDACTED]"

// DefaultRedact — поля и параметры, которые не попадают в outbound_request
// (05 §1, 07 §4). Сравнение без учёта регистра. Поле `code` сюда не входит:
// в ответах это код ошибки внешней системы, он нужен для разбора. Адаптер
// добавляет свои поля через Options.Redact (ИНН, почта, коды подключения).
var DefaultRedact = []string{"token", "access_token", "refresh_token", "authorization", "secret", "password", "api_key"}

// sensitiveHeaders не сохраняются вовсе.
var sensitiveHeaders = map[string]bool{"authorization": true, "cookie": true, "x-api-key": true, "proxy-authorization": true}

type redactor struct {
	keys          map[string]bool
	sensitiveCall bool
}

func newRedactor(extra []string) redactor {
	keys := map[string]bool{}
	for _, k := range DefaultRedact {
		keys[k] = true
	}
	for _, k := range extra {
		keys[strings.ToLower(k)] = true
	}
	return redactor{keys: keys}
}

func (r redactor) sensitive(key string) bool { return r.keys[strings.ToLower(key)] }

// url убирает значения чувствительных параметров запроса.
func (r redactor) url(raw string) string {
	if r.sensitiveCall {
		return redacted
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid url]"
	}
	u.User = nil
	q := u.Query()
	changed := false
	for k := range q {
		if r.sensitive(k) {
			q.Set(k, redacted)
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func (r redactor) headers(h http.Header) map[string]string {
	out := map[string]string{}
	if r.sensitiveCall {
		return out
	}
	for k, v := range h {
		lk := strings.ToLower(k)
		if sensitiveHeaders[lk] {
			continue
		}
		if r.sensitive(lk) {
			out[k] = redacted
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// value рекурсивно заменяет значения чувствительных полей JSON.
func (r redactor) value(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if r.sensitive(k) {
				t[k] = redacted
				continue
			}
			t[k] = r.value(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = r.value(t[i])
		}
		return t
	default:
		return v
	}
}

// body превращает тело в значение для jsonb: JSON — с маскированием, иное —
// строка с обрезкой; большое тело — только размер.
func (r redactor) body(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if r.sensitiveCall {
		return redacted
	}
	if len(b) > maxStoredBody {
		return map[string]any{"_truncated": true, "_size": len(b)}
	}
	var v any
	if err := json.Unmarshal(b, &v); err == nil {
		return r.value(v)
	}
	return string(b)
}

// bodyJSON — body, сериализованное для jsonb; nil — пустое тело.
func (r redactor) bodyJSON(b []byte) []byte {
	v := r.body(b)
	if v == nil {
		return nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return out
}

// text — тело для сообщения об ошибке и журнала: маскированное и короткое.
func (r redactor) text(b []byte, limit int) string {
	out := r.bodyJSON(b)
	if len(out) > limit {
		return string(out[:limit]) + "…"
	}
	return string(out)
}

// with — маскирование с дополнительными полями одного вызова.
func (r redactor) with(extra []string) redactor {
	if len(extra) == 0 {
		return r
	}
	keys := make(map[string]bool, len(r.keys)+len(extra))
	for k := range r.keys {
		keys[k] = true
	}
	for _, k := range extra {
		keys[strings.ToLower(k)] = true
	}
	return redactor{keys: keys, sensitiveCall: r.sensitiveCall}
}
