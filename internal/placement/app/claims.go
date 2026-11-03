package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// actionPlacementClaim — действие журнала претензии рекламодателя.
const actionPlacementClaim = "placement.claim"

// ClaimInput — претензия рекламодателя (S-08).
type ClaimInput struct {
	Reason  domain.ClaimReason
	Text    string
	IdemKey string
}

// CreateClaim — рекламодатель подаёт претензию (S-08, P-14): из published и из removed до
// claim_deadline_at, одна на размещение (R-1, R-2). В транзакции — претензия, спор с предложением
// доли (R-3), переход в disputed; после — оповещение администратору. Повтор с тем же ключом
// возвращает размещение без новой претензии (R-4). Текст претензии в журнал не пишется.
func (s *Service) CreateClaim(ctx context.Context, orgID, userID, id uuid.UUID, in ClaimInput) (PlacementView, error) {
	const op = "Placement.Claim"
	p0, err := s.Placements.GetForAdvertiser(ctx, orgID, id)
	if err != nil {
		return PlacementView{}, notFound(err, "placement")
	}
	var (
		out    domain.Placement
		opened *domain.Dispute
	)
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		o, err := s.Orders.GetByIDForUpdate(ctx, p0.OrderID)
		if err != nil {
			return err
		}
		p, err := s.Placements.GetForAdvertiserForUpdate(ctx, orgID, id)
		if err != nil {
			return notFound(err, "placement")
		}
		out = p
		if repeated, err := s.repeated(ctx, op, p, in.IdemKey); err != nil || repeated {
			return err
		}
		now := s.Clock.Now()
		c, err := domain.NewClaim(ids.New(), p.ID, userID, in.Reason, in.Text, now)
		if err != nil {
			return s.rejected(ctx, op, p, domain.P14OpenDispute, err)
		}
		if _, err := s.Claims.GetByPlacement(ctx, p.ID); err == nil {
			return s.rejected(ctx, op, p, domain.P14OpenDispute, domain.ErrClaimExists)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		before := p
		change, err := p.FileClaim(userID, in.Reason, now, s.Config.Control)
		if err != nil {
			return s.rejected(ctx, op, p, domain.P14OpenDispute, err)
		}
		share, err := s.claimShare(ctx, p, in.Reason)
		if err != nil {
			return err
		}
		if err := s.Claims.Insert(ctx, c); err != nil {
			return s.rejected(ctx, op, before, domain.P14OpenDispute, err)
		}
		d := domain.NewClaimDispute(ids.New(), c, share, now, s.Config.Control)
		if err := s.Disputes.Insert(ctx, d); err != nil {
			if errors.Is(err, domain.ErrDisputeExists) {
				return s.rejected(ctx, op, before, domain.P14OpenDispute, err)
			}
			return err
		}
		if err := s.apply(ctx, op, o, p, change, source{UserID: &userID, Key: in.IdemKey}); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionPlacementClaim, ObjectType: "placement", ObjectID: p.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"order_id": o.ID, "claim_id": c.ID, "reason": c.Reason, "from": change.From},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "placement claim filed", logx.KeyOp, op, KeyPlacementID, p.ID, KeyOrderID, o.ID,
			"claim_id", c.ID, "reason", c.Reason, "from", change.From)
		s.log.InfoContext(ctx, "placement dispute opened", logx.KeyOp, op, KeyPlacementID, p.ID, "opened_by", d.OpenedBy,
			"reason", d.Reason, "suggested_share", d.SuggestedShare, "deadline_at", d.DeadlineAt)
		out, opened = p, &d
		return s.refreshOrder(ctx, op, &o)
	})
	if err != nil {
		return PlacementView{}, err
	}
	if opened != nil {
		s.disputeOpened(ctx, op, *opened, "Рекламодатель подал претензию по размещению")
	}
	return s.placementView(ctx, out)
}

// claimShare — предложение доли по претензии (R-3): по публикации размещения; публикации нет —
// без предложения.
func (s *Service) claimShare(ctx context.Context, p domain.Placement, reason domain.ClaimReason) (*int, error) {
	pub, err := s.Publications.GetByPlacementForUpdate(ctx, p.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	removeAt := pub.PublishedAt
	if p.RemoveAt != nil {
		removeAt = *p.RemoveAt
	}
	return domain.ClaimSuggestedShare(reason, pub, removeAt, s.Config.Control), nil
}

// disputeOpened — после транзакции P-14: метрика и оповещение администратору placement_disputed.
func (s *Service) disputeOpened(ctx context.Context, op string, d domain.Dispute, summary string) {
	s.Metrics.DisputeOpened(d.OpenedBy, d.Reason)
	s.raise(ctx, op, alert.Alert{
		Reason: "placement_disputed", ObjectKey: d.PlacementID.String(), Level: alert.LevelWarn, Summary: summary,
		Fields: map[string]any{"placement_id": d.PlacementID, "reason": d.Reason, "opened_by": d.OpenedBy,
			"deadline_at": d.DeadlineAt},
	})
}
