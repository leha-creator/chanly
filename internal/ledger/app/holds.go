package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/ledger/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// HoldRequest — удержание под размещение с версией поста (R-6).
type HoldRequest struct {
	PlacementID   uuid.UUID
	PostVersionID uuid.UUID
	Amount        money.Kopecks
}

// HoldAll удерживает суммы размещений рекламодателя в транзакции вызывающего (R-6):
// размещения с активным удержанием пропускаются; сумма остальных сверяется с балансом под
// блокировкой — при нехватке ничего не пишется, *NegativeLiabilityError с общей нехваткой.
func (s *Service) HoldAll(ctx context.Context, orgID uuid.UUID, reqs []HoldRequest) error {
	const op = "Ledger.HoldAll"
	if !s.Store.InTransaction(ctx) {
		s.log.ErrorContext(ctx, "ledger called outside transaction", logx.KeyOp, op)
		return domain.ErrNoTransaction
	}
	if len(reqs) == 0 {
		return nil
	}
	placementIDs := make([]uuid.UUID, 0, len(reqs))
	refs := []domain.AccountRef{domain.AdvertiserBalance(orgID)}
	for _, r := range reqs {
		placementIDs = append(placementIDs, r.PlacementID)
		refs = append(refs, domain.PlacementHold(r.PlacementID))
	}
	now := s.Clock.Now()
	// Блокировка баланса до чтения удержаний: параллельное оформление ждёт здесь (R-5).
	accounts, err := s.Store.LockAccounts(ctx, refs, now)
	if err != nil {
		return err
	}
	active, err := s.Store.ActiveHolds(ctx, placementIDs)
	if err != nil {
		return err
	}
	var need money.Kopecks
	todo := make([]HoldRequest, 0, len(reqs))
	for _, r := range reqs {
		if _, ok := active[r.PlacementID]; ok {
			continue
		}
		need += r.Amount
		todo = append(todo, r)
	}
	if len(todo) == 0 {
		s.log.DebugContext(ctx, "holds already placed", logx.KeyOp, op, logx.KeyOrganizationID, orgID, "placements", len(reqs))
		return nil
	}
	balance := domain.AdvertiserBalance(orgID)
	if available := accounts[balance].Balance; need > available {
		s.log.WarnContext(ctx, "holds rejected", logx.KeyOp, op, logx.KeyOrganizationID, orgID, "placements", len(todo),
			KeyAmount, int64(need), "missing", int64(need-available), "reason", "insufficient_balance")
		return &domain.NegativeLiabilityError{Account: balance, Missing: need - available}
	}
	for _, r := range todo {
		res, err := s.Post(ctx, domain.NewHold(r.PlacementID, r.PostVersionID, orgID, r.Amount))
		if err != nil {
			return err
		}
		if res.Replayed {
			// Удержание этой версии уже проводилось и снято: повторное было бы без денег.
			return s.invariant(ctx, op, r.PlacementID, "hold replayed without active hold")
		}
		if err := s.Store.InsertHold(ctx, domain.Hold{
			ID: ids.New(), PlacementID: r.PlacementID, PostVersionID: r.PostVersionID, OrganizationID: orgID,
			Amount: r.Amount, Status: domain.HoldActive, CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	s.log.InfoContext(ctx, "holds placed", logx.KeyOp, op, logx.KeyOrganizationID, orgID, "placements", len(todo),
		KeyAmount, int64(need))
	return nil
}

// Release снимает остаток активного удержания размещения на баланс (R-6); без активного
// удержания ничего не делает. После снятия остаток placement_hold — ноль.
func (s *Service) Release(ctx context.Context, placementID uuid.UUID, reason string) error {
	const op = "Ledger.Release"
	if !s.Store.InTransaction(ctx) {
		s.log.ErrorContext(ctx, "ledger called outside transaction", logx.KeyOp, op)
		return domain.ErrNoTransaction
	}
	h, err := s.Store.ActiveHoldForUpdate(ctx, placementID)
	if errors.Is(err, domain.ErrNotFound) {
		s.log.DebugContext(ctx, "hold release skipped, no active hold", logx.KeyOp, op, KeyPlacementID, placementID,
			"reason", reason)
		return nil
	}
	if err != nil {
		return err
	}
	now := s.Clock.Now()
	ref := domain.PlacementHold(placementID)
	accounts, err := s.Store.LockAccounts(ctx, []domain.AccountRef{ref, domain.AdvertiserBalance(h.OrganizationID)}, now)
	if err != nil {
		return err
	}
	amount := accounts[ref].Balance
	if amount > 0 {
		if _, err := s.Post(ctx, domain.NewHoldRelease(placementID, h.PostVersionID, h.OrganizationID, amount)); err != nil {
			return err
		}
	}
	if err := s.closeHold(ctx, op, h, domain.HoldReleased); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "hold released", logx.KeyOp, op, KeyPlacementID, placementID, "reason", reason,
		KeyAmount, int64(amount))
	return nil
}

// Settle распределяет удержание размещения (R-8): блогеру, комиссия, возврат рекламодателю;
// удержание — distributed, остаток placement_hold — ноль. Повтор после распределения — без
// изменений. Сумма частей не равна остатку удержания — ErrInvariant.
func (s *Service) Settle(ctx context.Context, placementID, bloggerOrgID uuid.UUID, d domain.Distribution,
	actor domain.ActorType, actorUserID uuid.UUID) error {
	const op = "Ledger.Settle"
	if !s.Store.InTransaction(ctx) {
		s.log.ErrorContext(ctx, "ledger called outside transaction", logx.KeyOp, op)
		return domain.ErrNoTransaction
	}
	h, err := s.Store.ActiveHoldForUpdate(ctx, placementID)
	if errors.Is(err, domain.ErrNotFound) {
		if _, err := s.Store.TransactionByKey(ctx, domain.SettlementKey(placementID)); err == nil {
			s.log.DebugContext(ctx, "hold already distributed", logx.KeyOp, op, KeyPlacementID, placementID)
			return nil
		}
		return s.invariant(ctx, op, placementID, "settlement without active hold")
	}
	if err != nil {
		return err
	}
	ref := domain.PlacementHold(placementID)
	accounts, err := s.Store.LockAccounts(ctx, []domain.AccountRef{ref}, s.Clock.Now())
	if err != nil {
		return err
	}
	if held := accounts[ref].Balance; d.Total() != held {
		return s.invariant(ctx, op, placementID, fmt.Sprintf("distribution %d does not match hold %d", d.Total(), held))
	}
	if d.Total() > 0 {
		if _, err := s.Post(ctx, domain.NewSettlement(placementID, h.OrganizationID, bloggerOrgID, d, actor, actorUserID)); err != nil {
			return err
		}
	}
	if err := s.closeHold(ctx, op, h, domain.HoldDistributed); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "hold distributed", logx.KeyOp, op, KeyPlacementID, placementID,
		"payout_amount", int64(d.Payout), "fee_charged", int64(d.FeeCharged), "refund_amount", int64(d.Refund))
	return nil
}

// HasActive — у каких размещений есть активное удержание (R-7).
func (s *Service) HasActive(ctx context.Context, placementIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	active, err := s.Store.ActiveHolds(ctx, placementIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(placementIDs))
	for _, id := range placementIDs {
		_, out[id] = active[id]
	}
	return out, nil
}

// closeHold проверяет нулевой остаток placement_hold (06 §2.3, I-12) и закрывает удержание.
func (s *Service) closeHold(ctx context.Context, op string, h domain.Hold, status domain.HoldStatus) error {
	left, err := s.Store.AccountBalance(ctx, domain.PlacementHold(h.PlacementID))
	if err != nil {
		return err
	}
	if left != 0 {
		return s.invariant(ctx, op, h.PlacementID, fmt.Sprintf("placement hold balance %d after close", left))
	}
	return s.Store.CloseHold(ctx, h.ID, status, s.Clock.Now())
}

// invariant — ERROR о нарушении правила журнала; транзакция вызывающего откатывается.
func (s *Service) invariant(ctx context.Context, op string, placementID uuid.UUID, detail string) error {
	s.log.ErrorContext(ctx, "ledger invariant violated", logx.KeyOp, op, KeyPlacementID, placementID, "detail", detail)
	return fmt.Errorf("%w: %s: %s", domain.ErrInvariant, placementID, detail)
}
