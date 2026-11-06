package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestNewOrganizationRequest(t *testing.T) {
	id := uuid.MustParse("0190a000-0000-7000-8000-000000000010")
	cases := []struct {
		legal, inn, kpp string
		isRr, platform  bool
		wantType        string
		wantKPP         string
		wantRr          bool
	}{
		{LegalEntity, "7707083893", "770701001", false, false, OrdTypeUL, "770701001", false},
		{SoleProprietor, "500100732259", "", false, false, OrdTypeIP, "", false},
		{SelfEmployed, "500100732259", "770701001", true, false, OrdTypeFL, "", true},
		// R-5: платформа никогда не рекламораспространитель.
		{SoleProprietor, "500100732259", "", true, true, OrdTypeIP, "", false},
	}
	for _, c := range cases {
		org := OrgDetails{ID: id, LegalType: c.legal, INN: c.inn, Name: "Орг", KPP: c.kpp, IsPlatform: c.platform}
		req, err := NewOrganizationRequest(org, c.isRr)
		if err != nil {
			t.Fatalf("%s: %v", c.legal, err)
		}
		if req.Type != c.wantType || req.KPP != c.wantKPP || req.IsRr != c.wantRr || req.IsOrs || req.ID != id.String() {
			t.Errorf("%s: %+v", c.legal, req)
		}
	}
	long := OrgDetails{ID: id, LegalType: LegalEntity, INN: "7707083893", Name: strings.Repeat("я", 256)}
	_, err := NewOrganizationRequest(long, false)
	if !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("long name: %v", err)
	}
	if reason, ok := BuildFailureReason(err); !ok || reason != ReasonNameTooLong {
		t.Fatalf("BuildFailureReason = %q, %v", reason, ok)
	}
	if _, err := NewOrganizationRequest(OrgDetails{ID: id, LegalType: "unknown", Name: "x"}, false); !errors.Is(err, ErrPermanent) {
		t.Fatalf("unknown legal type: %v", err)
	}
}

func TestNewContractRequest(t *testing.T) {
	rules := ContractRules{}.WithDefaults()
	adv, plat, blog := uuid.New(), uuid.New(), uuid.New()
	// 23:30 UTC 16-го — 02:30 МСК 17-го (R-7).
	date := ContractDate(time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC))
	if date.Format(time.DateOnly) != "2026-10-17" {
		t.Fatalf("contract date %s, want 2026-10-17", date.Format(time.DateOnly))
	}
	client, contractor := ContractParties(ContractInitial, plat, adv)
	initial := Contract{ID: uuid.New(), Kind: ContractInitial, ClientOrganizationID: client,
		ContractorOrganizationID: contractor, SignedDate: date, Number: ContractNumber("AG-", 3)}
	req := NewContractRequest(initial, rules.Params(ContractInitial))
	if req.Type != OrdIntermediaryContract || req.AgentActingForPublisher == nil || *req.AgentActingForPublisher ||
		req.ClientID != adv.String() || req.ContractorID != plat.String() || !req.IsRegReport ||
		req.Date != "2026-10-17" || req.Number != "AG-3" || req.SubjectType != "mediation" || req.ActionType != "conclude" {
		t.Fatalf("initial: %+v", req)
	}
	client, contractor = ContractParties(ContractPublisher, plat, blog)
	pub := Contract{ID: uuid.New(), Kind: ContractPublisher, ClientOrganizationID: client,
		ContractorOrganizationID: contractor, SignedDate: date}
	if pub.OfferOrganization() != blog || initial.OfferOrganization() != adv {
		t.Fatal("offer organization: initial — advertiser, publisher — blogger")
	}
	req = NewContractRequest(pub, rules.Params(ContractPublisher))
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "agentActingForPublisher") || strings.Contains(string(raw), "number") {
		t.Fatalf("publisher contract body: %s", raw)
	}
	if req.Type != OrdContract || req.ClientID != plat.String() || req.ContractorID != blog.String() {
		t.Fatalf("publisher: %+v", req)
	}
	if OfferKind(ContractPublisher) != "blogger_offer" || OfferKind(ContractInitial) != "advertiser_offer" {
		t.Fatal("OfferKind")
	}
	if ContractNumber("", 3) != "" {
		t.Fatal("empty prefix must give no number")
	}
}

func TestNewPlatformRequest(t *testing.T) {
	name := PlatformName("  " + strings.Repeat("к", 150))
	if utf8.RuneCountInString(name) != 100 || strings.HasPrefix(name, " ") {
		t.Fatalf("platform name has %d runes", utf8.RuneCountInString(name))
	}
	p := Platform{ID: uuid.New(), ChannelID: uuid.New(), OrganizationID: uuid.New(), Name: name, URL: "https://max.ru/join/abc"}
	req := NewPlatformRequest(p, "site")
	item := req.Platforms[0]
	if req.OrganizationID != p.OrganizationID.String() || item.PlatformID != p.ChannelID.String() || item.IsOwned ||
		item.Type != "site" || item.URL != p.URL {
		t.Fatalf("platform request: %+v", req)
	}
}

func TestCreativeDescriptionV8Limit(t *testing.T) {
	c := NewCreative(uuid.New(), uuid.New(), t0)
	text := strings.Repeat("Товар ", 200)
	req := NewCreativeRequest(c, "contract-1", CreativeContent{Text: text}, "30.15.1")
	if utf8.RuneCountInString(req.Description) > 1000 || !utf8.ValidString(req.Description) ||
		req.Description != strings.TrimSpace(string([]rune(text)[:1000])) {
		t.Fatalf("description does not satisfy v8 limit: %q", req.Description)
	}
	if req.TextData[0] != strings.TrimSpace(text) {
		t.Fatal("description limit truncated the creative text")
	}
}

func TestNewCreativeRequest(t *testing.T) {
	c := NewCreative(uuid.New(), uuid.New(), t0)
	content := CreativeContent{Text: "  Скидки до 50%  ", URLs: []string{"https://shop.ru/?utm_source=maxim"}}
	req := NewCreativeRequest(c, "contract-1", content, "30.15.1")
	if req.Form != FormTextBlock || len(req.MediaData) != 0 || req.TextData[0] != "Скидки до 50%" || req.Description != "Скидки до 50%" ||
		req.CreativeType != "creative" || req.CoBranding || req.IsSocial || req.IsSocialQuota ||
		len(req.ContractIDs) != 1 || req.ContractIDs[0] != "contract-1" || req.KKTUCodes[0] != "30.15.1" ||
		req.ID != c.ID.String() || req.URLs[0] != content.URLs[0] {
		t.Fatalf("text-only creative: %+v", req)
	}
	content.ImageURLs = []string{"https://cdn/1.png", "https://cdn/2.jpg"}
	req = NewCreativeRequest(c, "contract-1", content, "30.15.1")
	if req.Form != FormTextGraphicBlock || len(req.MediaData) != 2 || req.MediaData[0].MediaURL != "https://cdn/1.png" ||
		req.MediaData[1].MediaURL != "https://cdn/2.jpg" || req.MediaData[0].MediaURLFileType != "image" {
		t.Fatalf("creative with images: %+v", req)
	}
	if (CreativeContent{ImageURLs: []string{"a", "", ""}}).PendingImages() != 2 {
		t.Fatal("PendingImages")
	}
	if !c.SetErid("tok", t0) || c.SetErid("other", t0) || c.Erid != "tok" {
		t.Fatal("SetErid writes the token once (D-03)")
	}
	c.Sync.Fail("rejected", true)
	c.ApplyErir(OutcomeFailed)
	if c.ErirStatus != ErirError || c.ErirError != "rejected" {
		t.Fatalf("ApplyErir: %+v", c)
	}
}

func TestDependencyVerdict(t *testing.T) {
	ready := SyncState{Status: SyncSynced}
	accepted := SyncState{Status: SyncSent, OrdStatus: OrdSuccess}
	inWork := SyncState{Status: SyncSent, OrdStatus: OrdRequestAccepted}
	failed := SyncState{Status: SyncError}
	need := CreativeDependencies()
	if len(need) != 3 || need[2] != NeedInitialContract {
		t.Fatalf("creative chain %v", need)
	}
	cases := []struct {
		name              string
		deps              []Dependency
		missing           int
		waiting, isFailed bool
		ready             bool
	}{
		{"all ready", []Dependency{{Need: need[0], Exists: true, State: ready}, {Need: need[1], Exists: true, State: accepted},
			{Need: need[2], Exists: true, State: ready}}, 0, false, false, true},
		{"missing", []Dependency{{Need: need[0], Exists: true, State: ready}, {Need: need[1]}, {Need: need[2]}}, 2, false, false, false},
		{"waiting", []Dependency{{Need: need[0], Exists: true, State: inWork}, {Need: need[1], Exists: true, State: ready},
			{Need: need[2], Exists: true, State: NewSyncState()}}, 0, true, false, false},
		{"failed", []Dependency{{Need: need[0], Exists: true, State: ready}, {Need: need[1], Exists: true, State: failed},
			{Need: need[2], Exists: true, State: inWork}}, 0, true, true, false},
	}
	for _, c := range cases {
		v := DependencyVerdict(c.deps)
		if len(v.Missing) != c.missing || (v.Waiting != nil) != c.waiting || (v.Failed != nil) != c.isFailed || v.Ready() != c.ready {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
	if d := ContractDependencies(ContractPublisher); d[1] != NeedBloggerOrganization {
		t.Fatalf("publisher chain %v", d)
	}
	if d := PlatformDependencies(); len(d) != 1 || d[0] != NeedBloggerOrganization {
		t.Fatalf("platform chain %v", d)
	}
}
