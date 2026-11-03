package clientbank_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/integration/bank/clientbank"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

func parse(t *testing.T, name string) domain.Statement {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	st, err := clientbank.New(0, logx.Discard()).Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return st
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// Сбер, windows-1251: остатки, исходящий платёж отброшен, суммы с копейками и без.
func TestSber(t *testing.T) {
	st := parse(t, "sber.txt")
	if st.Account != "40802810938000000001" || *st.Opening != 15000000 || *st.Closing != 17614567 ||
		!st.DateFrom.Equal(date(2026, 10, 19)) || !st.DateTo.Equal(date(2026, 10, 19)) {
		t.Fatalf("statement %+v", st)
	}
	if len(st.Incoming) != 2 {
		t.Fatalf("incoming %d", len(st.Incoming))
	}
	first, second := st.Incoming[0], st.Incoming[1]
	if first.Amount != 1234567 || first.PayerINN != "7707083893" || first.PayerName != `ООО "РОМАШКА"` ||
		first.PayerAccount != "40702810400000012345" || !first.OperationDate.Equal(date(2026, 10, 19)) ||
		!strings.Contains(first.Purpose, "MX-000101") {
		t.Fatalf("first %+v", first)
	}
	// Без Плательщик1 — наименование из «ИНН … имя»; без копеек.
	if second.Amount != 1500000 || second.PayerName != "Иванов Иван Иванович" || !strings.Contains(second.Purpose, "МХ-000102") {
		t.Fatalf("second %+v", second)
	}
	if first.ExternalID == second.ExternalID || len(first.ExternalID) != 64 {
		t.Fatalf("external ids %q %q", first.ExternalID, second.ExternalID)
	}
	if want := clientbank.ExternalID(date(2026, 10, 19), "512", 1234567, "7707083893", "40702810400000012345"); first.ExternalID != want {
		t.Fatal("external id is not R-12 hash")
	}
}

// Точка, windows-1251: ДатаПоступило пустая — Дата; повтор документа — тот же external_id.
func TestTochkaDuplicate(t *testing.T) {
	st := parse(t, "tochka.txt")
	if len(st.Incoming) != 2 || st.Incoming[0].ExternalID != st.Incoming[1].ExternalID {
		t.Fatalf("incoming %+v", st.Incoming)
	}
	if in := st.Incoming[0]; in.Amount != 500050 || !in.OperationDate.Equal(date(2026, 10, 2)) {
		t.Fatalf("incoming %+v", in)
	}
	if *st.Opening != 0 || *st.Closing != 500050 || !st.DateFrom.Equal(date(2026, 10, 1)) {
		t.Fatalf("statement %+v", st)
	}
}

// Т-Банк, UTF-8: неизвестные ключи пропускаются; CRLF и BOM не мешают.
func TestTBankUTF8(t *testing.T) {
	st := parse(t, "tbank.txt")
	if len(st.Incoming) != 1 || st.Incoming[0].Amount != 100000 || st.Incoming[0].PayerName != `ООО "СЕВЕР"` {
		t.Fatalf("incoming %+v", st.Incoming)
	}
	data, _ := os.ReadFile(filepath.Join("testdata", "tbank.txt"))
	crlf := append([]byte("\xef\xbb\xbf"), bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))...)
	again, err := clientbank.New(0, nil).Parse(crlf)
	if err != nil || len(again.Incoming) != 1 || again.Incoming[0].ExternalID != st.Incoming[0].ExternalID {
		t.Fatalf("crlf: %v %+v", err, again)
	}
}

// Кодировка=DOS — cp866.
func TestDOSEncoding(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "tbank.txt"))
	dos, err := charmap.CodePage866.NewEncoder().Bytes(bytes.Replace(data, []byte("Кодировка=UTF-8"), []byte("Кодировка=DOS"), 1))
	if err != nil {
		t.Fatal(err)
	}
	st, err := clientbank.New(0, nil).Parse(dos)
	if err != nil || len(st.Incoming) != 1 || st.Incoming[0].PayerName != `ООО "СЕВЕР"` {
		t.Fatalf("dos: %v %+v", err, st)
	}
}

func TestBrokenFiles(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "tbank.txt"))
	good := string(data)
	cases := map[string]struct {
		text string
		line string
	}{
		"no header":      {strings.Replace(good, "1CClientBankExchange", "Выписка", 1), "line 1:"},
		"bad amount":     {strings.Replace(good, "Сумма=1000.00", "Сумма=1000.001", 1), "line 13:"},
		"bad date":       {strings.Replace(good, "ДатаПоступило=19.10.2026", "ДатаПоступило=2026-10-19", 1), "line 13:"},
		"no end":         {strings.Replace(good, "КонецДокумента\n", "", 1), "line 24: unclosed section"},
		"bad balance":    {strings.Replace(good, "НачальныйОстаток=100.00", "НачальныйОстаток=сто", 1), "line 10:"},
		"garbage line":   {strings.Replace(good, "Номер=31", "Номер 31", 1), "line 14:"},
		"nested section": {strings.Replace(good, "КонецРасчСчет", "СекцияДокумент=х", 1), "line 12:"},
		"empty":          {"", "line 1:"},
	}
	for name, c := range cases {
		_, err := clientbank.New(0, nil).Parse([]byte(c.text))
		if !errors.Is(err, domain.ErrStatementFormat) || !strings.Contains(err.Error(), c.line) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := clientbank.New(100, nil).Parse(data); !errors.Is(err, domain.ErrStatementTooLarge) {
		t.Errorf("too large: %v", err)
	}
}

func TestExternalIDStable(t *testing.T) {
	a := clientbank.ExternalID(date(2026, 10, 19), " 12 ", money.Kopecks(100), "7707083893", "1")
	b := clientbank.ExternalID(date(2026, 10, 19), "12", money.Kopecks(100), "7707083893", "1")
	if a != b || a == clientbank.ExternalID(date(2026, 10, 19), "12", money.Kopecks(101), "7707083893", "1") {
		t.Fatal("external id")
	}
}
