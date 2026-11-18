package app

import (
	"context"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/support/domain"
	"testing"
)

func TestInvalidOperationsDoNotReachRepository(t *testing.T) {
	s := &Service{Log: logx.Discard()}
	if e := s.Read(context.Background(), ids.New(), ids.New(), -1); e == nil {
		t.Fatal("negative marker accepted")
	}
	if _, e := s.Update(context.Background(), ids.New(), ids.New(), 1, "invalid", nil); e == nil {
		t.Fatal("invalid status accepted")
	}
	items, n, e := s.List(context.Background(), domain.Filter{})
	if e != nil || n != 0 || len(items) != 0 {
		t.Fatal("disabled support queried repository")
	}
}
