package domain

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leha-creator/chanly/internal/platform/money"
)

func intPtr(v int) *int { return &v }

func TestCPV(t *testing.T) {
	for _, tc := range []struct {
		spent money.Kopecks
		views *int
		want  *money.Kopecks
	}{
		{1_000_000, nil, nil},
		{1_000_000, intPtr(0), nil},
		{0, intPtr(100), nil},
		{1_000_000, intPtr(1), ptrKopecks(1_000_000)},
		{1_000_000, intPtr(3), ptrKopecks(333_333)}, // вниз
		{100, intPtr(1000), ptrKopecks(0)},
	} {
		got := CPV(tc.spent, tc.views)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("CPV(%d, %v) = %v, want %v", tc.spent, tc.views, got, tc.want)
		}
	}
}

func ptrKopecks(v money.Kopecks) *money.Kopecks { return &v }

func reportFixture() Report {
	published := mskTime(2026, 10, 21, 10, 0)
	removed := mskTime(2026, 10, 22, 10, 0)
	paid := testPlacement(StatusPaid)
	paid.TotalAmount, paid.RefundAmount = 1_150_000, ptrKopecks(150_000)
	paid.PublishedAt, paid.RemovedAt, paid.FinalViews = &published, &removed, intPtr(1900)

	disputed := testPlacement(StatusDisputed)
	disputed.TotalAmount, disputed.PublishedAt = 230_000, &published
	pub := &Publication{URL: "https://max.ru/c/1/2", PublishedAt: published, Views: intPtr(400)}

	cancelled := testPlacement(StatusCancelled)
	cancelled.TotalAmount = 500_000

	o := Order{Number: 1001}
	return BuildReport(o, "Осень", []string{"https://shop.test/?utm_campaign=order_1001"}, "max", "channel_ad", []ReportInput{
		{Placement: *paid, Channel: ChannelRef{Title: "Кухня; рецепты", Link: "https://max.ru/kitchen"}},
		{Placement: *disputed, Channel: ChannelRef{Title: "Бизнес"}, Publication: pub, HoldActive: true},
		{Placement: *cancelled, Channel: ChannelRef{Title: "Отменён"}},
	})
}

func TestBuildReport(t *testing.T) {
	r := reportFixture()
	paid, disputed, cancelled := r.Rows[0], r.Rows[1], r.Rows[2]
	if paid.Spent != 1_000_000 || paid.Refunded != 150_000 || paid.Held != 0 || paid.CPV == nil || *paid.CPV != 526 {
		t.Fatalf("paid row %+v", paid)
	}
	if disputed.Held != 230_000 || disputed.Spent != 0 || disputed.CPV != nil || *disputed.Views != 400 ||
		disputed.PublicationURL != "https://max.ru/c/1/2" {
		t.Fatalf("disputed row %+v", disputed)
	}
	if cancelled.Refunded != 500_000 || cancelled.Views != nil || cancelled.PublishedAt != nil {
		t.Fatalf("cancelled row %+v", cancelled)
	}
	tt := r.Totals
	if tt.Placements != 3 || tt.Published != 2 || tt.Views != 2300 || tt.Spent != 1_000_000 || tt.Refunded != 650_000 ||
		tt.Held != 230_000 || tt.CPV == nil || *tt.CPV != 434 {
		t.Fatalf("totals %+v", tt)
	}
	if r.UTM != (UTMMarks{Source: "max", Medium: "channel_ad", Campaign: "order_1001"}) || len(r.Links) != 1 {
		t.Fatalf("utm %+v links %v", r.UTM, r.Links)
	}
	if empty := BuildReport(Order{Number: 7}, "", nil, "max", "channel_ad", nil); empty.Links == nil || empty.Totals.CPV != nil {
		t.Fatalf("empty report %+v", empty)
	}
}

func TestWriteReportCSV(t *testing.T) {
	var b bytes.Buffer
	if err := WriteReportCSV(&b, reportFixture()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.HasPrefix(out, utf8BOM+"Канал;") {
		t.Fatalf("no BOM or header: %q", out[:20])
	}
	lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
	if len(lines) != 5 {
		t.Fatalf("lines %d: %q", len(lines), out)
	}
	want := `"Кухня; рецепты";https://max.ru/kitchen;Выплачено;21.10.2026 10:00;22.10.2026 10:00;1900;5,26;10000,00;1500,00;0,00;;order_1001`
	if lines[1] != want {
		t.Fatalf("row\n got %s\nwant %s", lines[1], want)
	}
	if !strings.HasPrefix(lines[4], "Итого;;Размещений: 3, опубликовано: 2;;;2300;4,34;10000,00;6500,00;2300,00;;order_1001") {
		t.Fatalf("totals %s", lines[4])
	}
}

func TestSafeCell(t *testing.T) {
	for in, want := range map[string]string{"=HYPERLINK(1)": "'=HYPERLINK(1)", "+7": "'+7", "-x": "'-x", "@a": "'@a",
		"Кухня": "Кухня", "": "", "https://max.ru/c/1": "https://max.ru/c/1"} {
		if got := safeCell(in); got != want {
			t.Errorf("safeCell(%q) = %q", in, got)
		}
	}
}
