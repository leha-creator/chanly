// Package clientbank — разбор выписки в формате обмена «1С:Клиент-банк»
// (1CClientBankExchange, 05 §5, A-35): заголовок, секция расчётного счёта с остатками и
// документы. Поступление — документ, где ПолучательСчет равен счёту выписки (R-12).
// Кодировка — windows-1251 (Кодировка=Windows), cp866 (Кодировка=DOS) или UTF-8.
package clientbank

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// DefaultMaxBytes — предел размера файла по умолчанию (STATEMENT_MAX_BYTES).
const DefaultMaxBytes = 10 << 20

const (
	header         = "1CClientBankExchange"
	accountSection = "СекцияРасчСчет"
	accountEnd     = "КонецРасчСчет"
	documentPrefix = "СекцияДокумент"
	documentEnd    = "КонецДокумента"
	fileEnd        = "КонецФайла"
	dateLayout     = "02.01.2006"
)

// Parser — реализация billing/domain.StatementParser.
type Parser struct {
	// MaxBytes — предел размера файла; 0 — DefaultMaxBytes.
	MaxBytes int
	Log      *slog.Logger
}

var _ domain.StatementParser = Parser{}

// New создаёт разборщик с пределом размера maxBytes.
func New(maxBytes int, log *slog.Logger) Parser {
	return Parser{MaxBytes: maxBytes, Log: log}
}

// document — поля документа, нужные для поступления.
type document struct {
	line   int
	fields map[string]string
}

// Parse разбирает файл выписки. Ошибка разбора — ErrStatementFormat с номером строки;
// неизвестные ключи пропускаются.
func (p Parser) Parse(data []byte) (domain.Statement, error) {
	limit := p.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	if len(data) > limit {
		return domain.Statement{}, fmt.Errorf("%w: %d bytes, limit %d", domain.ErrStatementTooLarge, len(data), limit)
	}
	text := decode(data)

	var (
		st        domain.Statement
		headAcc   string
		docs      []document
		doc       *document
		inAccount bool
		started   bool
		finished  bool
	)
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !started {
			if line != header {
				return domain.Statement{}, formatError(n, "missing "+header+" header")
			}
			started = true
			continue
		}
		if finished {
			return domain.Statement{}, formatError(n, "content after "+fileEnd)
		}
		key, value, hasValue := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case key == documentPrefix:
			if doc != nil || inAccount {
				return domain.Statement{}, formatError(n, "unclosed section")
			}
			doc = &document{line: n, fields: map[string]string{}}
		case key == documentEnd && !hasValue:
			if doc == nil {
				return domain.Statement{}, formatError(n, documentEnd+" without "+documentPrefix)
			}
			docs = append(docs, *doc)
			doc = nil
		case key == accountSection && !hasValue:
			if doc != nil || inAccount {
				return domain.Statement{}, formatError(n, "unclosed section")
			}
			inAccount = true
		case key == accountEnd && !hasValue:
			if !inAccount {
				return domain.Statement{}, formatError(n, accountEnd+" without "+accountSection)
			}
			inAccount = false
		case key == fileEnd && !hasValue:
			if doc != nil || inAccount {
				return domain.Statement{}, formatError(n, "unclosed section")
			}
			finished = true
		case !hasValue:
			return domain.Statement{}, formatError(n, "expected key=value")
		case doc != nil:
			doc.fields[key] = value
		case inAccount:
			if err := accountField(&st, key, value); err != nil {
				return domain.Statement{}, formatError(n, err.Error())
			}
		default:
			if err := headerField(&st, &headAcc, key, value); err != nil {
				return domain.Statement{}, formatError(n, err.Error())
			}
		}
	}
	if err := sc.Err(); err != nil {
		return domain.Statement{}, fmt.Errorf("%w: line %d: %v", domain.ErrStatementFormat, n+1, err)
	}
	if !started {
		return domain.Statement{}, formatError(1, "empty file")
	}
	if doc != nil || inAccount {
		return domain.Statement{}, formatError(n, "unexpected end of file")
	}
	if st.Account == "" {
		st.Account = headAcc
	}
	if st.Account == "" {
		return domain.Statement{}, formatError(n, "no account number")
	}
	for _, d := range docs {
		if d.fields["ПолучательСчет"] != st.Account || d.fields["ПлательщикСчет"] == st.Account {
			continue
		}
		in, err := incoming(d)
		if err != nil {
			return domain.Statement{}, err
		}
		st.Incoming = append(st.Incoming, in)
	}
	if p.Log != nil {
		p.Log.Debug("statement parsed", logx.KeyOp, "Billing.ParseStatement", "documents", len(docs),
			"incoming", len(st.Incoming), "account", logx.MaskAccount(st.Account))
	}
	return st, nil
}

// headerField — поле заголовка: счёт (первый) и период.
func headerField(st *domain.Statement, acc *string, key, value string) error {
	switch key {
	case "РасчСчет":
		if *acc == "" {
			*acc = value
		}
	case "ДатаНачала", "ДатаКонца":
		return dateField(st, key, value)
	}
	return nil
}

// accountField — поле секции расчётного счёта: счёт, период и остатки (R-12). Секций несколько —
// берётся первая по счёту; остатки — начальный первой и конечный последней.
func accountField(st *domain.Statement, key, value string) error {
	switch key {
	case "РасчСчет":
		if st.Account == "" {
			st.Account = value
		}
	case "ДатаНачала", "ДатаКонца":
		return dateField(st, key, value)
	case "НачальныйОстаток", "КонечныйОстаток":
		if value == "" {
			return nil
		}
		k, err := money.Parse(value)
		if err != nil {
			return fmt.Errorf("%s: invalid amount %q", key, value)
		}
		if key == "НачальныйОстаток" {
			if st.Opening == nil {
				st.Opening = &k
			}
		} else {
			st.Closing = &k
		}
	}
	return nil
}

func dateField(st *domain.Statement, key, value string) error {
	if value == "" {
		return nil
	}
	d, err := parseDate(value)
	if err != nil {
		return fmt.Errorf("%s: invalid date %q", key, value)
	}
	if key == "ДатаНачала" {
		if st.DateFrom == nil || d.Before(*st.DateFrom) {
			st.DateFrom = &d
		}
	} else if st.DateTo == nil || d.After(*st.DateTo) {
		st.DateTo = &d
	}
	return nil
}

// incoming — поступление из документа; дата — ДатаПоступило, пустая — Дата.
func incoming(d document) (domain.Incoming, error) {
	f := d.fields
	amount, err := money.Parse(f["Сумма"])
	if err != nil || amount <= 0 {
		return domain.Incoming{}, formatError(d.line, fmt.Sprintf("document: invalid amount %q", f["Сумма"]))
	}
	raw := f["ДатаПоступило"]
	if raw == "" {
		raw = f["Дата"]
	}
	date, err := parseDate(raw)
	if err != nil {
		return domain.Incoming{}, formatError(d.line, fmt.Sprintf("document: invalid date %q", raw))
	}
	name := f["Плательщик1"]
	if name == "" {
		name = payerName(f["Плательщик"])
	}
	in := domain.Incoming{
		OperationDate: date,
		Amount:        amount,
		PayerINN:      f["ПлательщикИНН"],
		PayerName:     name,
		PayerAccount:  f["ПлательщикСчет"],
		Purpose:       f["НазначениеПлатежа"],
	}
	in.ExternalID = ExternalID(date, f["Номер"], amount, in.PayerINN, in.PayerAccount)
	in.DocumentNumber = strings.TrimSpace(f["Номер"])
	if d, err := parseDate(f["Дата"]); err == nil {
		in.DocDate = &d
	}
	in.ChargeDate = &date
	return in, nil
}

// ExternalID — идентификатор поступления (R-12): в формате 1С уникального идентификатора нет,
// поэтому SHA-256 от даты поступления, номера, суммы, ИНН и счёта плательщика.
func ExternalID(date time.Time, number string, amount money.Kopecks, payerINN, payerAccount string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		date.Format(time.DateOnly), strings.TrimSpace(number), strconv.FormatInt(int64(amount), 10), payerINN, payerAccount,
	}, "|")))
	return hex.EncodeToString(sum[:])
}

// payerName — наименование из «ИНН 7707083893 ООО "Ромашка"».
func payerName(s string) string {
	if rest, ok := strings.CutPrefix(s, "ИНН "); ok {
		if _, name, ok := strings.Cut(rest, " "); ok {
			return strings.TrimSpace(name)
		}
	}
	return s
}

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation(dateLayout, strings.TrimSpace(s), time.UTC)
}

func formatError(line int, msg string) error {
	return fmt.Errorf("%w: line %d: %s", domain.ErrStatementFormat, line, msg)
}

// decode переводит файл в UTF-8: корректный UTF-8 (с BOM или без) остаётся как есть,
// Кодировка=DOS — cp866, иначе windows-1251.
func decode(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) {
		return string(data)
	}
	if dos, err := charmap.CodePage866.NewDecoder().Bytes(data); err == nil && bytes.Contains(dos, []byte("Кодировка=DOS")) {
		return string(dos)
	}
	win, _ := charmap.Windows1251.NewDecoder().Bytes(data)
	return string(win)
}
