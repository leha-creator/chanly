package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

const (
	defaultPageLimit = 20
	maxPageLimit     = 100
)

// PayoutItem — строка blg/payouts: попытка, заказ и канал.
type PayoutItem struct {
	Workflow *WorkflowView
	domain.Payout
	Brief domain.PlacementBrief
}

// PayoutPage — страница выплат; NextCursor пуст на последней странице.
type PayoutPage struct {
	Items      []PayoutItem
	NextCursor string
	Total      int
}

type cursorJSON struct {
	At time.Time `json:"a"`
	ID uuid.UUID `json:"i"`
}

func encodeCursor(c domain.Cursor) string {
	raw, _ := json.Marshal(cursorJSON{At: c.At, ID: c.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (*domain.Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, apperr.Validation("cursor", "invalid")
	}
	var c cursorJSON
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == uuid.Nil {
		return nil, apperr.Validation("cursor", "invalid")
	}
	return &domain.Cursor{At: c.At, ID: c.ID}, nil
}

// List — выплаты организации-блогера, новые сверху (listBloggerPayouts).
func (s *Service) List(ctx context.Context, orgID uuid.UUID, cursor string, limit int) (PayoutPage, error) {
	after, err := decodeCursor(cursor)
	if err != nil {
		return PayoutPage{}, err
	}
	if limit <= 0 {
		limit = defaultPageLimit
	}
	limit = min(limit, maxPageLimit)
	rows, err := s.Store.List(ctx, orgID, after, limit+1)
	if err != nil {
		return PayoutPage{}, err
	}
	var page PayoutPage
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(domain.Cursor{At: last.CreatedAt, ID: last.ID})
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, p := range rows {
		ids = append(ids, p.PlacementID)
	}
	briefs, err := s.Placements.Brief(ctx, ids)
	if err != nil {
		return PayoutPage{}, fmt.Errorf("payout list briefs: %w", err)
	}
	for _, p := range rows {
		view, err := s.workflowView(ctx, p)
		if err != nil {
			return PayoutPage{}, err
		}
		page.Items = append(page.Items, PayoutItem{Payout: p, Brief: briefs[p.PlacementID], Workflow: view})
	}
	sum, err := s.Store.Summary(ctx, orgID)
	if err != nil {
		return PayoutPage{}, err
	}
	page.Total = sum.Count
	s.log.DebugContext(ctx, "payouts listed", logx.KeyOp, "Payout.List", logx.KeyOrganizationID, orgID, "count", len(page.Items))
	return page, nil
}

// Summary — итоги blg/payouts: получено и в работе (getBloggerPayoutSummary).
func (s *Service) Summary(ctx context.Context, orgID uuid.UUID) (domain.Summary, error) {
	return s.Store.Summary(ctx, orgID)
}
