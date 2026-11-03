package adminpage_test

import (
	"testing"

	"github.com/leha-creator/chanly/internal/platform/adminpage"
)

func TestPage(t *testing.T) {
	limit := 2
	p, err := adminpage.Parse(nil, &limit)
	if err != nil || p.Offset != 0 || p.Limit != 2 {
		t.Fatalf("first page %+v %v", p, err)
	}
	next := p.Next(5)
	if next == nil {
		t.Fatal("next cursor missing")
	}
	p2, err := adminpage.Parse(next, &limit)
	if err != nil || p2.Offset != 2 {
		t.Fatalf("second page %+v %v", p2, err)
	}
	p3, _ := adminpage.Parse(p2.Next(5), &limit)
	if p3.Next(5) != nil {
		t.Fatal("last page has next cursor")
	}
	bad := "x"
	if _, err := adminpage.Parse(&bad, nil); err == nil {
		t.Fatal("bad cursor accepted")
	}
	big := 1000
	if p, _ := adminpage.Parse(nil, &big); p.Limit != adminpage.MaxLimit {
		t.Fatalf("limit %d", p.Limit)
	}
}
