// Package adminpage — постраничный вывод списков администратора: курсор — смещение
// в выборке с общим числом строк (total). Списки администратора небольшие, сортировка
// у них составная (срок, время ожидания), поэтому курсор по ключу не нужен.
package adminpage

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// Пределы страницы (PageMeta спецификации).
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

const prefix = "o:"

// Page — запрошенная страница: смещение и размер.
type Page struct {
	Offset int
	Limit  int
}

// Parse разбирает курсор и размер; неверный курсор — 422 cursor.
func Parse(cursor *string, limit *int) (Page, error) {
	p := Page{Limit: DefaultLimit}
	if limit != nil && *limit > 0 {
		p.Limit = min(*limit, MaxLimit)
	}
	if cursor == nil || *cursor == "" {
		return p, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(*cursor)
	if err != nil || !strings.HasPrefix(string(raw), prefix) {
		return Page{}, apperr.Validation("cursor", "invalid")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(raw), prefix))
	if err != nil || n < 0 {
		return Page{}, apperr.Validation("cursor", "invalid")
	}
	p.Offset = n
	return p, nil
}

// Next — курсор следующей страницы; nil — страница последняя.
func (p Page) Next(total int) *string {
	next := p.Offset + p.Limit
	if next >= total {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString([]byte(prefix + strconv.Itoa(next)))
	return &s
}

// moscow — даты фильтров администратора по Москве.
var moscow = time.FixedZone("MSK", 3*60*60)

// DayStart — начало дня d по Москве со сдвигом на days дней (для «по» включительно — days = 1);
// nil — фильтра нет.
func DayStart(d *time.Time, days int) *time.Time {
	if d == nil {
		return nil
	}
	t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, moscow).AddDate(0, 0, days).UTC()
	return &t
}
