package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// GenerateResult — итог формирования закрывающих документов за период.
type GenerateResult struct {
	Created int
	Skipped int
	Failed  int
}

// ClosingActPage — страница закрывающих документов; NextCursor пуст на последней странице.
type ClosingActPage struct {
	Items      []domain.ClosingAct
	NextCursor string
}

// GenerateClosingActs формирует отчёт агента и акт за период (R-16) каждой организации с
// распределениями удержаний в периоде: у каждой — своя транзакция, уже сформированные
// пропускаются, сбой одной не мешает остальным (ошибка возвращается после всех).
func (s *Service) GenerateClosingActs(ctx context.Context, p domain.Period) (GenerateResult, error) {
	const op = "Billing.GenerateClosingActs"
	start := s.Clock.Now()
	settlements, err := s.Ledger.Settlements(ctx, p.Start(), p.End())
	if err != nil {
		return GenerateResult{}, err
	}
	byOrg := map[uuid.UUID][]domain.Settlement{}
	var orgs []uuid.UUID
	for _, st := range settlements {
		if _, ok := byOrg[st.AdvertiserOrgID]; !ok {
			orgs = append(orgs, st.AdvertiserOrgID)
		}
		byOrg[st.AdvertiserOrgID] = append(byOrg[st.AdvertiserOrgID], st)
	}
	var res GenerateResult
	var errs []error
	for _, org := range orgs {
		created, err := s.closingAct(ctx, op, p, org, byOrg[org])
		switch {
		case err != nil:
			res.Failed++
			errs = append(errs, err)
			s.log.ErrorContext(ctx, "closing act failed", logx.KeyOp, op, logx.KeyOrganizationID, org, "period", p.String(),
				logx.KeyError, err.Error())
		case created:
			res.Created++
		default:
			res.Skipped++
		}
	}
	s.log.InfoContext(ctx, "closing acts generated", logx.KeyOp, op, "period", p.String(), "organizations", len(orgs),
		"created", res.Created, "skipped", res.Skipped, "failed", res.Failed, "elapsed_ms", s.Clock.Now().Sub(start).Milliseconds())
	return res, errors.Join(errs...)
}

// closingAct — документы одной организации; false — уже сформированы.
func (s *Service) closingAct(ctx context.Context, op string, p domain.Period, orgID uuid.UUID, settlements []domain.Settlement) (bool, error) {
	exists, err := s.ClosingActs.Exists(ctx, orgID, p)
	if err != nil || exists {
		return false, err
	}
	placementIDs := make([]uuid.UUID, 0, len(settlements))
	for _, st := range settlements {
		placementIDs = append(placementIDs, st.PlacementID)
	}
	info, err := s.Placements.Info(ctx, placementIDs)
	if err != nil {
		return false, err
	}
	lines := make([]domain.ReportLine, 0, len(settlements))
	for _, st := range settlements {
		pi := info[st.PlacementID]
		lines = append(lines, domain.ReportLine{PlacementID: st.PlacementID, OrderNumber: pi.OrderNumber,
			ChannelTitle: pi.ChannelTitle, PublishedAt: pi.PublishedAt, RemovedAt: pi.RemovedAt, Payout: st.Payout, Fee: st.Fee,
			SettledAt: st.SettledAt})
	}
	placements, fee := domain.ReportTotals(lines)
	org, err := s.Organizations.Requisites(ctx, orgID)
	if err != nil {
		return false, err
	}
	seq, err := s.ClosingActs.NextNumber(ctx)
	if err != nil {
		return false, err
	}
	now := s.Clock.Now()
	act := domain.ClosingAct{
		ID: ids.New(), OrganizationID: orgID, Period: p, Number: domain.FormatActNumber(seq), PlacementsAmount: placements,
		FeeAmount: fee, CreatedAt: now,
	}
	prefix := fmt.Sprintf("closing/%s/%s", orgID, p)
	act.ReportKey, act.ActKey = prefix+"-report.pdf", prefix+"-act.pdf"
	report, err := s.Renderer.AgentReport(domain.ReportDoc{Number: act.Number, Period: p, IssuedAt: now,
		Supplier: s.Config.Supplier, Principal: org, Lines: lines})
	if err != nil {
		return false, err
	}
	actPDF, err := s.Renderer.Act(domain.ActDoc{Number: act.Number, Period: p, IssuedAt: now, Supplier: s.Config.Supplier,
		Customer: org, FeeAmount: fee, VATNote: s.Config.VATNote})
	if err != nil {
		return false, err
	}
	for key, data := range map[string][]byte{act.ReportKey: report, act.ActKey: actPDF} {
		if err := s.Storage.PutPrivate(ctx, key, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
			return false, err
		}
	}
	created := false
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		// Повторная проверка: параллельный запуск мог успеть раньше.
		if exists, err := s.ClosingActs.Exists(ctx, orgID, p); err != nil || exists {
			return err
		}
		if err := s.ClosingActs.Insert(ctx, act); err != nil {
			return err
		}
		created = true
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: ActionClosingActGenerated, ObjectType: "closing_act", ObjectID: act.ID.String(), ActorType: audit.ActorSystem,
			OrganizationID: ptr(orgID), Data: map[string]any{"number": act.Number, "period": p.String(),
				"placements_amount": int64(placements), "fee_amount": int64(fee), "placements": len(lines)},
		}); err != nil {
			return err
		}
		// Уведомление — в транзакции документа (I-10).
		return s.Notifier.Notify(ctx, domain.Notice{Template: domain.NoticeClosingActsReady, OrganizationID: orgID,
			ObjectType: "closing_act", ObjectID: act.ID.String(), Data: map[string]any{"number": act.Number, "period": p.String()}})
	})
	if err != nil || !created {
		return false, err
	}
	s.log.InfoContext(ctx, "closing act created", logx.KeyOp, op, KeyClosingActID, act.ID, logx.KeyOrganizationID, orgID,
		"placements_amount", int64(placements), "fee_amount", int64(fee))
	return true, nil
}

// ListClosingActs — закрывающие документы организации, новые периоды сверху.
func (s *Service) ListClosingActs(ctx context.Context, orgID uuid.UUID, cursorStr string, limit int) (ClosingActPage, error) {
	after, err := cursor(cursorStr)
	if err != nil {
		return ClosingActPage{}, err
	}
	limit = pageLimit(limit)
	list, err := s.ClosingActs.List(ctx, orgID, after, limit+1)
	if err != nil {
		return ClosingActPage{}, err
	}
	page := ClosingActPage{}
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		page.NextCursor = domain.Cursor{At: last.Period.Date(), ID: last.ID}.Encode()
	}
	page.Items = list
	return page, nil
}

// ClosingActPDF — отчёт агента или акт своей организации; вызывающий закрывает поток.
func (s *Service) ClosingActPDF(ctx context.Context, orgID, id uuid.UUID, doc domain.ClosingDocument) (domain.ClosingAct, io.ReadCloser, int64, error) {
	var key string
	act, err := s.ClosingActs.Get(ctx, orgID, id)
	if err != nil {
		return domain.ClosingAct{}, nil, 0, notFound(err, "closing act")
	}
	switch doc {
	case domain.DocumentReport:
		key = act.ReportKey
	case domain.DocumentAct:
		key = act.ActKey
	default:
		return domain.ClosingAct{}, nil, 0, apperr.Validation("document", "invalid")
	}
	r, size, err := s.Storage.Get(ctx, key)
	if err != nil {
		return domain.ClosingAct{}, nil, 0, err
	}
	return act, r, size, nil
}
