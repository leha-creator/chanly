package domain

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

const rkn = 10000

var (
	formats = []Format{
		{ID: "f_1_24", IsActive: true, Position: 0},
		{ID: "f_2_48", IsActive: true, Position: 1},
		{ID: "f_3_72", IsActive: true, Position: 2},
	}
	admin = uuid.MustParse("0190a000-0000-7000-8000-0000000000ad")
)

// priced — канал в состоянии s с включённой ценой f_1_24.
func priced(s Status) *Channel {
	c := channelIn(s)
	ready := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	c.OrdPlatformReadyAt = &ready
	c.Prices = []Price{{FormatID: "f_1_24", Amount: 300000, IsEnabled: true, FormatActive: true}}
	if c.CategoryID == nil {
		cat := uuid.New()
		c.CategoryID = &cat
	}
	return c
}

func TestTotalAmount(t *testing.T) {
	for _, tc := range []struct {
		amount money.Kopecks
		bp     int
		want   money.Kopecks
	}{
		{10000, 0, 10000}, {10000, 1500, 11500}, {10000, 10000, 20000},
		{10001, 0, 10001}, {10001, 1500, 11501}, {10001, 10000, 20002},
		{1_000_000_000, 0, 1_000_000_000}, {1_000_000_000, 1500, 1_150_000_000}, {1_000_000_000, 10000, 2_000_000_000},
	} {
		if got := TotalAmount(tc.amount, tc.bp); got != tc.want {
			t.Errorf("TotalAmount(%d, %d) = %d, want %d", tc.amount, tc.bp, got, tc.want)
		}
	}
}

func TestSetPricesByStatus(t *testing.T) {
	set := []Price{{FormatID: "f_2_48", Amount: 450000, IsEnabled: true}, {FormatID: "f_1_24", Amount: 300000, IsEnabled: false}}
	for _, s := range Statuses {
		c := priced(s)
		err := c.SetPrices(set, formats, t0)
		switch s {
		case StatusUnclaimed, StatusDisconnected, StatusBlocked:
			if !errors.Is(err, ErrNotEditable) {
				t.Errorf("%s: %v, want ErrNotEditable", s, err)
			}
		default:
			if err != nil {
				t.Errorf("%s: %v", s, err)
				continue
			}
			// Порядок — по справочнику, а не по запросу.
			if len(c.Prices) != 2 || c.Prices[0].FormatID != "f_1_24" || c.Prices[1].FormatID != "f_2_48" || !c.Prices[1].FormatActive {
				t.Errorf("%s: prices %+v", s, c.Prices)
			}
		}
	}
}

func TestSetPricesRules(t *testing.T) {
	off := []Price{{FormatID: "f_1_24", Amount: 300000, IsEnabled: false}}
	for _, tc := range []struct {
		name   string
		status Status
		prices []Price
		want   error
	}{
		{"duplicate", StatusModeration, []Price{{FormatID: "f_1_24", Amount: 10000}, {FormatID: "f_1_24", Amount: 20000}}, ErrDuplicateFormat},
		{"unknown", StatusModeration, []Price{{FormatID: "f_9_99", Amount: 10000}}, ErrUnknownFormat},
		{"below", StatusModeration, []Price{{FormatID: "f_1_24", Amount: 9999}}, ErrPriceOutOfRange},
		{"above", StatusModeration, []Price{{FormatID: "f_1_24", Amount: 1_000_000_001}}, ErrPriceOutOfRange},
		{"bounds", StatusModeration, []Price{{FormatID: "f_1_24", Amount: 10000}, {FormatID: "f_2_48", Amount: 1_000_000_000}}, nil},
		{"active all off", StatusActive, off, ErrActiveRequiresPrice},
		{"moderation all off", StatusModeration, off, nil},
		{"suspended all off", StatusSuspended, off, nil},
	} {
		c := priced(tc.status)
		before := slices.Clone(c.Prices)
		err := c.SetPrices(tc.prices, formats, t0)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if err != nil && !slices.Equal(c.Prices, before) {
			t.Errorf("%s: prices changed on error", tc.name)
		}
	}
	inactive := []Format{{ID: "f_1_24", IsActive: false}}
	if err := priced(StatusModeration).SetPrices([]Price{{FormatID: "f_1_24", Amount: 10000}}, inactive, t0); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("inactive format: %v", err)
	}
}

func TestValidateUnavailability(t *testing.T) {
	day := 24 * time.Hour
	iv := func(from, to time.Duration) Unavailability {
		return Unavailability{ID: uuid.New(), StartsAt: t0.Add(from), EndsAt: t0.Add(to)}
	}
	existing := []Unavailability{iv(10*day, 12*day), iv(-5*day, -time.Hour)} // второй уже прошёл
	many := make([]Unavailability, 0, 50)
	for i := range 50 {
		many = append(many, iv(time.Duration(100+i*2)*day, time.Duration(101+i*2)*day))
	}
	for _, tc := range []struct {
		name     string
		u        Unavailability
		existing []Unavailability
		want     error
	}{
		{"ok", iv(day, 2*day), existing, nil},
		{"empty", iv(day, day), nil, ErrUnavailabilityInPast},
		{"reversed", iv(2*day, day), nil, ErrUnavailabilityInPast},
		{"ended", iv(-2*day, -day), nil, ErrUnavailabilityInPast},
		{"ends now", iv(-day, 0), nil, ErrUnavailabilityInPast},
		{"started, still live", iv(-day, day), nil, nil},
		{"90 days", iv(day, 91*day), nil, nil},
		{"too long", iv(day, 91*day+time.Second), nil, ErrUnavailabilityTooLong},
		{"365 days ahead", iv(365*day, 366*day), nil, nil},
		{"too far", iv(365*day+time.Second, 366*day), nil, ErrUnavailabilityTooFar},
		{"overlap", iv(11*day, 13*day), existing, ErrUnavailabilityOverlap},
		{"inside", iv(10*day+time.Hour, 11*day), existing, ErrUnavailabilityOverlap},
		{"touch end", iv(12*day, 13*day), existing, nil},
		{"touch start", iv(9*day, 10*day), existing, nil},
		{"past interval ignored", iv(-4*day, day), existing, nil},
		{"too many", iv(day, 2*day), many, ErrUnavailabilityTooMany},
		{"49 live", iv(day, 2*day), many[:49], nil},
	} {
		if err := ValidateUnavailability(tc.u, tc.existing, t0, UnavailabilityLimits{}); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestCatalogVisible(t *testing.T) {
	for _, subs := range []int{9999, 10000} {
		for _, st := range []RknStatus{RknNotRequired, RknPending, RknConfirmed, RknRejected} {
			c := priced(StatusActive)
			c.SubscribersCount, c.RknStatus = subs, st
			want := subs < rkn || st == RknConfirmed
			if got := CatalogVisible(c, rkn); got != want {
				t.Errorf("subs %d, rkn %s: visible %v, want %v", subs, st, got, want)
			}
		}
	}
	noPrice := priced(StatusActive)
	noPrice.Prices[0].IsEnabled = false
	inactiveFormat := priced(StatusActive)
	inactiveFormat.Prices[0].FormatActive = false
	noPlatform := priced(StatusActive)
	noPlatform.OrdPlatformReadyAt = nil
	for name, c := range map[string]*Channel{
		"no enabled price": noPrice, "inactive format": inactiveFormat, "no ord platform": noPlatform,
		"moderation": priced(StatusModeration), "suspended": priced(StatusSuspended),
	} {
		if CatalogVisible(c, rkn) {
			t.Errorf("%s: visible", name)
		}
	}
}

func TestApprove(t *testing.T) {
	ok := priced(StatusModeration)
	change, err := ok.Approve(admin, rkn, t0)
	if err != nil || ok.Status != StatusActive || change.From != StatusModeration || change.To != StatusActive {
		t.Fatalf("approve: %v %+v", err, change)
	}
	if ok.ModeratedAt == nil || !ok.ModeratedAt.Equal(t0) || ok.ModeratedBy == nil || *ok.ModeratedBy != admin {
		t.Fatalf("moderated_at/by not set: %+v", ok)
	}

	breakers := map[string]func(c *Channel){
		MissingRights:   func(c *Channel) { c.BotPermissions = []string{"write"} },
		MissingCategory: func(c *Channel) { c.CategoryID = nil },
		MissingPrice:    func(c *Channel) { c.Prices[0].IsEnabled = false },
		MissingRkn:      func(c *Channel) { c.SubscribersCount, c.RknStatus = 12000, RknPending },
	}
	order := []string{MissingRights, MissingCategory, MissingPrice, MissingRkn}
	// Каждое условие по отдельности и все сочетания.
	for mask := 1; mask < 1<<len(order); mask++ {
		c := priced(StatusModeration)
		var want []string
		for i, name := range order {
			if mask&(1<<i) != 0 {
				breakers[name](c)
				want = append(want, name)
			}
		}
		_, err := c.Approve(admin, rkn, t0)
		var blocked *ApproveBlockedError
		if !errors.As(err, &blocked) || !errors.Is(err, ErrApproveBlocked) || !slices.Equal(blocked.Missing, want) {
			t.Errorf("missing %v: got %v", want, err)
		}
		if c.Status != StatusModeration || c.ModeratedAt != nil {
			t.Errorf("missing %v: channel changed: %s", want, c.Status)
		}
	}
	notPublic := priced(StatusModeration)
	notPublic.IsPublic = false
	if _, err := notPublic.Approve(admin, rkn, t0); err == nil {
		t.Error("closed channel approved")
	}
	confirmed := priced(StatusModeration)
	confirmed.SubscribersCount, confirmed.RknStatus = 12000, RknConfirmed
	if _, err := confirmed.Approve(admin, rkn, t0); err != nil {
		t.Errorf("confirmed rkn: %v", err)
	}
	for _, s := range Statuses {
		if s == StatusModeration {
			continue
		}
		if _, err := priced(s).Approve(admin, rkn, t0); !errors.Is(err, ErrNotInModeration) {
			t.Errorf("approve from %s: %v", s, err)
		}
	}
}

func TestPrivateChannelPolicyPreservesFactsAndRights(t *testing.T) {
	c := priced(StatusModeration)
	c.IsPublic = false
	if _, err := c.Approve(admin, rkn, t0, AllowPrivateChannels); err != nil {
		t.Fatal(err)
	}
	private := pub
	private.IsPublic = false
	c.MetricsRefreshed(private, rkn, t0, AllowPrivateChannels)
	if c.Status != StatusActive || c.IsPublic {
		t.Fatalf("metrics lost private eligibility/fact: %s public=%v", c.Status, c.IsPublic)
	}
	c.RightsRefreshed(private, Membership{IsAdmin: true, Permissions: []string{"write"}}, rkn, t0, AllowPrivateChannels)
	if c.Status != StatusSuspended || c.StatusReason != ReasonMissingRights {
		t.Fatal("private policy bypassed delete permission")
	}
	c.RightsRefreshed(private, allOK, rkn, t0, AllowPrivateChannels)
	if c.Status != StatusActive {
		t.Fatal("private channel did not resume with rights")
	}
	c.MetricsRefreshed(private, rkn, t0)
	if c.Status != StatusSuspended || c.StatusReason != ReasonNotPublic {
		t.Fatal("default policy accepted private channel")
	}
}

func TestReject(t *testing.T) {
	c := priced(StatusModeration)
	change, err := c.Reject("  В описании нет тематики  ", t0)
	if err != nil || c.Status != StatusRejected || c.StatusReason != "В описании нет тематики" || change.To != StatusRejected {
		t.Fatalf("reject: %v %+v", err, c)
	}
	long := make([]rune, RejectReasonMaxLength+1)
	for i := range long {
		long[i] = 'я'
	}
	for _, reason := range []string{"", "   ", string(long)} {
		if _, err := priced(StatusModeration).Reject(reason, t0); !errors.Is(err, ErrRejectReason) {
			t.Errorf("reason of %d runes: %v", len([]rune(reason)), err)
		}
	}
	if _, err := priced(StatusModeration).Reject(string(long[:RejectReasonMaxLength]), t0); err != nil {
		t.Errorf("500 runes: %v", err)
	}
	for _, s := range []Status{StatusActive, StatusPendingRights, StatusRejected, StatusSuspended} {
		if _, err := priced(s).Reject("нет", t0); !errors.Is(err, ErrNotInModeration) {
			t.Errorf("reject from %s: %v", s, err)
		}
	}
}

func TestRknRule(t *testing.T) {
	info := func(subs int) ChatInfo {
		return ChatInfo{Title: "Канал", IsPublic: true, IsChannel: true, SubscribersCount: subs}
	}

	// active 9 999 → 10 000 без номера → suspended/rkn_required.
	c := priced(StatusActive)
	c.SubscribersCount = 9999
	change := c.MetricsRefreshed(info(10000), rkn, t0)
	if c.Status != StatusSuspended || c.StatusReason != ReasonRknRequired || !change.AvailabilityLost() || c.RknStatus != RknPending {
		t.Fatalf("crossing threshold: %s/%s rkn %s, %+v", c.Status, c.StatusReason, c.RknStatus, change)
	}
	// Повтор с тем же числом ничего не меняет.
	if again := c.MetricsRefreshed(info(10000), rkn, t0); again.Changed() {
		t.Fatalf("repeat changed status: %+v", again)
	}
	// Отказ проверки — канал остаётся приостановленным.
	c.RknNumber = "4930000000"
	if _, err := c.RknChecked(RknCheckRejected, admin, rkn, t0); err != nil || c.Status != StatusSuspended || c.RknStatus != RknRejected {
		t.Fatalf("rkn rejected: %v %s", err, c.Status)
	}
	// Подтверждение — снова active без модерации.
	back, err := c.RknChecked(RknCheckConfirmed, admin, rkn, t0)
	if err != nil || c.Status != StatusActive || c.StatusReason != "" || !back.AvailabilityRestored() {
		t.Fatalf("rkn confirmed: %v %s %+v", err, c.Status, back)
	}
	if c.RknCheckedAt == nil || c.RknCheckedBy == nil || *c.RknCheckedBy != admin {
		t.Fatal("rkn_checked_at/by not set")
	}

	// Падение ниже порога снимает rkn_required.
	down := priced(StatusSuspended)
	down.StatusReason, down.SubscribersCount, down.RknStatus = ReasonRknRequired, 10500, RknPending
	down.MetricsRefreshed(info(9999), rkn, t0)
	if down.Status != StatusActive || down.RknStatus != RknNotRequired {
		t.Fatalf("below threshold: %s rkn %s", down.Status, down.RknStatus)
	}

	// Права пропали у канала с rkn_required: причина — права; подтверждение номера её не снимает.
	rights := priced(StatusActive)
	rights.RknNumber = "4930000000"
	rights.RightsRefreshed(info(12000), Membership{IsAdmin: true, Permissions: []string{"write"}}, rkn, t0)
	if rights.Status != StatusSuspended || rights.StatusReason != ReasonMissingRights {
		t.Fatalf("rights lost: %s/%s", rights.Status, rights.StatusReason)
	}
	if _, err := rights.RknChecked(RknCheckConfirmed, admin, rkn, t0); err != nil || rights.StatusReason != ReasonMissingRights {
		t.Fatalf("rkn confirm removed missing_rights: %v %s", err, rights.StatusReason)
	}
	rights.RightsRefreshed(info(12000), allOK, rkn, t0)
	if rights.Status != StatusActive {
		t.Fatalf("rights back with confirmed rkn: %s/%s", rights.Status, rights.StatusReason)
	}

	// Права вернулись, но номер не подтверждён: канал переходит на причину rkn_required.
	both := priced(StatusSuspended)
	both.SubscribersCount, both.RknStatus = 12000, RknPending
	both.RightsRefreshed(info(12000), allOK, rkn, t0)
	if both.Status != StatusSuspended || both.StatusReason != ReasonRknRequired {
		t.Fatalf("rights back without rkn: %s/%s", both.Status, both.StatusReason)
	}

	// Приостановка по другой причине не снимается.
	other := priced(StatusSuspended)
	other.StatusReason = "admin_hold"
	other.MetricsRefreshed(info(100), rkn, t0)
	if other.Status != StatusSuspended || other.StatusReason != "admin_hold" {
		t.Fatalf("other reason resumed: %s/%s", other.Status, other.StatusReason)
	}

	// Каждое состояние × порог: только active и suspended приостанавливаются по РКН.
	for _, s := range Statuses {
		for _, subs := range []int{9999, 10000} {
			c := priced(s)
			if s == StatusUnclaimed {
				c.OrganizationID = nil
			}
			c.MetricsRefreshed(info(subs), rkn, t0)
			wantRkn := subs >= rkn && (s == StatusActive || s == StatusSuspended)
			if got := c.StatusReason == ReasonRknRequired; got != wantRkn {
				t.Errorf("%s with %d subscribers: %s/%s", s, subs, c.Status, c.StatusReason)
			}
		}
	}
}

func TestRknCheckedNotApplicable(t *testing.T) {
	for _, s := range []Status{StatusUnclaimed, StatusDisconnected} {
		c := priced(s)
		c.RknNumber = "1"
		if _, err := c.RknChecked(RknCheckConfirmed, admin, rkn, t0); !errors.Is(err, ErrRknNotCheckable) {
			t.Errorf("%s: %v", s, err)
		}
	}
	if _, err := priced(StatusModeration).RknChecked(RknCheckConfirmed, admin, rkn, t0); !errors.Is(err, ErrRknNotCheckable) {
		t.Error("check without number accepted")
	}
}

func TestRknRecheckDue(t *testing.T) {
	interval := 2160 * time.Hour
	c := priced(StatusActive)
	c.RknStatus = RknConfirmed
	checked := t0
	c.RknCheckedAt = &checked
	if c.RknRecheckDue(t0.Add(interval), interval) {
		t.Error("due exactly at 90 days")
	}
	if !c.RknRecheckDue(t0.Add(interval+time.Minute), interval) {
		t.Error("not due after 90 days + 1 min")
	}
	c.RknStatus = RknPending
	if c.RknRecheckDue(t0.Add(2*interval), interval) {
		t.Error("due without confirmation")
	}
}

func TestComputeMetrics(t *testing.T) {
	views := func(v int) *int { return &v }
	msg := func(age time.Duration, v *int) MessageStat {
		return MessageStat{ID: age.String(), At: t0.Add(-age), Views: v}
	}
	h := time.Hour
	cfg := MetricsConfig{}

	empty := ComputeMetrics(5000, nil, nil, t0, cfg)
	if empty.AvgViews != nil || empty.EngagementBP != nil || empty.PostsPerWeek != nil || len(empty.AnomalyFlags) != 0 || empty.SubscribersCount != 5000 {
		t.Fatalf("no messages: %+v", empty)
	}
	if !empty.Date.Equal(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("date %v", empty.Date)
	}
	noStat := ComputeMetrics(5000, []MessageStat{msg(48*h, nil), msg(72*h, nil)}, nil, t0, cfg)
	if noStat.AvgViews != nil || noStat.PostsPerWeek != nil {
		t.Fatalf("no stat.views: %+v", noStat)
	}

	// Сообщения моложе суток в среднее не входят, но считаются в частоте публикаций.
	fresh := ComputeMetrics(5000, []MessageStat{msg(2*h, views(100000)), msg(30*h, views(1000)), msg(50*h, views(2000))}, nil, t0, cfg)
	if fresh.AvgViews == nil || *fresh.AvgViews != 1500 || *fresh.EngagementBP != 3000 || *fresh.PostsPerWeek != 0.75 {
		t.Fatalf("fresh excluded: %+v", fresh)
	}
	// Старше 30 суток — вне окна; старше 28 — не в частоте.
	window := ComputeMetrics(5000, []MessageStat{msg(29*24*h, views(900)), msg(31*24*h, views(100000))}, nil, t0, cfg)
	if *window.AvgViews != 900 || *window.PostsPerWeek != 0 {
		t.Fatalf("window: %+v", window)
	}

	// Больше 100 сообщений — в среднее входят 100 последних.
	var many []MessageStat
	for i := range 150 {
		v := 1000
		if i >= 100 {
			v = 0 // старые сообщения не должны тянуть среднее вниз
		}
		many = append(many, msg(time.Duration(25+i)*h, views(v)))
	}
	if m := ComputeMetrics(5000, many, nil, t0, cfg); *m.AvgViews != 1000 {
		t.Fatalf("sample of 100: %d", *m.AvgViews)
	}

	// Рост подписчиков: 19,99 % — нет флага, 20,01 % — есть.
	prev := &MetricsSnapshot{SubscribersCount: 10000}
	if m := ComputeMetrics(11999, nil, prev, t0, cfg); slices.Contains(m.AnomalyFlags, FlagSubscribersSpike) {
		t.Error("spike at 19.99%")
	}
	if m := ComputeMetrics(12001, nil, prev, t0, cfg); !slices.Contains(m.AnomalyFlags, FlagSubscribersSpike) {
		t.Error("no spike at 20.01%")
	}
	if m := ComputeMetrics(50000, nil, nil, t0, cfg); slices.Contains(m.AnomalyFlags, FlagSubscribersSpike) {
		t.Error("spike without previous snapshot")
	}

	// avg_views 0,99 % подписчиков — флаг; 1 % — нет; больше подписчиков — флаг.
	old := func(v int) []MessageStat { return []MessageStat{msg(48*h, views(v))} }
	if m := ComputeMetrics(10000, old(99), nil, t0, cfg); !slices.Contains(m.AnomalyFlags, FlagViewsDisproportion) {
		t.Error("no flag at 0.99%")
	}
	if m := ComputeMetrics(10000, old(100), nil, t0, cfg); slices.Contains(m.AnomalyFlags, FlagViewsDisproportion) {
		t.Error("flag at 1%")
	}
	if m := ComputeMetrics(10000, old(10001), nil, t0, cfg); !slices.Contains(m.AnomalyFlags, FlagViewsDisproportion) {
		t.Error("no flag when avg_views > subscribers")
	}
	// Подписчиков нет: вовлечённость не считается.
	if m := ComputeMetrics(0, old(5), nil, t0, cfg); m.EngagementBP != nil || !slices.Contains(m.AnomalyFlags, FlagViewsDisproportion) {
		t.Errorf("zero subscribers: %+v", m)
	}
}

func TestCatalogCursor(t *testing.T) {
	v := int64(42)
	c := CatalogCursor{Sort: SortCPVAsc, Value: &v, ID: uuid.New()}
	got, err := DecodeCatalogCursor(c.Encode(), SortCPVAsc)
	if err != nil || *got.Value != 42 || got.ID != c.ID {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for name, s := range map[string]string{"other sort": c.Encode(), "garbage": "!!", "not json": "bm90IGpzb24", "empty": ""} {
		sort := SortCPVAsc
		if name == "other sort" {
			sort = SortPriceAsc
		}
		if _, err := DecodeCatalogCursor(s, sort); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (CatalogFilter{PriceFrom: ptr(money.Kopecks(2)), PriceTo: ptr(money.Kopecks(1))}).Validate(); !errors.Is(err, ErrPriceRange) {
		t.Errorf("price range: %v", err)
	}
	if err := (CatalogFilter{SubscribersFrom: ptr(5), SubscribersTo: ptr(4)}).Validate(); !errors.Is(err, ErrSubscribers) {
		t.Errorf("subscribers range: %v", err)
	}
	if cpv := CPV(1150, ptr(100)); cpv == nil || *cpv != 11 {
		t.Errorf("cpv %v", cpv)
	}
	if CPV(1150, ptr(0)) != nil || CPV(1150, nil) != nil {
		t.Error("cpv without views")
	}
}

func ptr[T any](v T) *T { return &v }
