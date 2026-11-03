package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Ежемесячная отчётность ОРД (этап «API администратора и отчётность ОРД», R-18 … R-24, 05 §3.7):
// снимок показов 1-го числа, формирование статистики и актов прошедшего месяца, отправка записей
// через sync_ord_object, проверка срока.

// Задания отчётности (07 §2).
const (
	KindMonthlyReport = "ord_monthly_report"
	KindReportCheck   = "ord_report_check"
	alertReportDue    = "ord_report_overdue"
	// ReasonReportNotBuilt — отправка несформированного отчёта.
	ReasonReportNotBuilt = "report_not_built"
	actionReportBuilt    = "ord_report.built"
	actionReportSend     = "ord_report.send_requested"
)

// ReportState — состояние отчёта для экрана (S-31).
type ReportState string

const (
	ReportNotBuilt  ReportState = "not_built"
	ReportBuilt     ReportState = "built"
	ReportSending   ReportState = "sending"
	ReportSent      ReportState = "sent"
	ReportHasErrors ReportState = "has_errors"
)

// ReportStatisticsRow — строка статистики для экрана.
type ReportStatisticsRow struct {
	Statistics domain.Statistics
	Erid       string
	ChannelID  uuid.UUID
	Channel    string
}

// ReportInvoiceRow — акт для экрана: сторона договора (рекламодатель или блогер).
type ReportInvoiceRow struct {
	Invoice        domain.Invoice
	Kind           domain.ContractKind
	OrganizationID uuid.UUID
}

// ReportView — отчёт месяца.
type ReportView struct {
	Period          domain.Period
	State           ReportState
	Built           *domain.ReportState
	Statistics      []ReportStatisticsRow
	Invoices        []ReportInvoiceRow
	Impressions     int64
	Amount          int64
	PendingCount    int
	ErrorCount      int
	StatisticsCount int
	InvoicesCount   int
}

func (s *Service) reports() (domain.Reports, error) {
	if s.Reports == nil || s.Source == nil {
		return nil, errors.New("marking: ORD reporting is not wired")
	}
	return s.Reports, nil
}

// TakeSnapshot снимает показы живых публикаций на начало месяца period (R-19): повтор не меняет
// снятых. Возвращает число новых снимков.
func (s *Service) TakeSnapshot(ctx context.Context, period domain.Period) (int, error) {
	const op = "Marking.TakeSnapshot"
	store, err := s.reports()
	if err != nil {
		return 0, err
	}
	live, err := s.Source.LivePublications(ctx, period.Start())
	if err != nil {
		return 0, err
	}
	var n int
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		n, err = store.InsertSnapshots(ctx, period, live, s.Clock.Now())
		return err
	})
	if err != nil {
		return 0, err
	}
	s.log.InfoContext(ctx, "ord views snapshot taken", logx.KeyOp, op, "period", period.String(), "publications", len(live), "new", n)
	return n, nil
}

// BuildReport формирует статистику и акты прошедшего месяца (R-20, R-21). Изменившиеся записи
// снова ждут отправки; ничего не отправляет. adminID — ручное формирование (журнал действий).
func (s *Service) BuildReport(ctx context.Context, period domain.Period, adminID *uuid.UUID, reason string) (*ReportView, error) {
	const op = "Marking.BuildReport"
	store, err := s.reports()
	if err != nil {
		return nil, err
	}
	now := s.Clock.Now()
	if !period.Closed(now) {
		return nil, apperr.Validation("period", domain.ReasonPeriodNotClosed).WithDetail("reason", domain.ReasonPeriodNotClosed)
	}
	if adminID != nil {
		ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: adminID})
	}
	source, err := s.Source.Placements(ctx, period)
	if err != nil {
		return nil, err
	}
	var stats struct{ created, changed, unchanged, removed, invoices int }
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		placements, err := s.reportPlacements(ctx, store, period, source)
		if err != nil {
			return err
		}
		lines := domain.BuildStatistics(placements, period)
		if err := s.saveStatistics(ctx, store, period, lines, &stats.created, &stats.changed, &stats.unchanged, &stats.removed); err != nil {
			return err
		}
		n, err := s.saveInvoices(ctx, store, period, domain.BuildInvoices(lines))
		if err != nil {
			return err
		}
		stats.invoices = n
		state, err := store.Report(ctx, period)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		state.Period, state.BuiltAt, state.BuiltBy, state.UpdatedAt = period, now, adminID, now
		if err := store.SaveReport(ctx, state); err != nil {
			return err
		}
		if adminID == nil {
			return nil
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionReportBuilt, ObjectType: "ord_report", ObjectID: period.String(), ActorType: audit.ActorAdmin,
			Reason: &reason, Data: map[string]any{"statistics": len(lines), "changed": stats.changed, "created": stats.created,
				"invoices": stats.invoices},
		})
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "ord report built", logx.KeyOp, op, "period", period.String(), "statistics_created", stats.created,
		"statistics_changed", stats.changed, "statistics_unchanged", stats.unchanged, "statistics_zeroed", stats.removed,
		"invoices", stats.invoices, "manual", adminID != nil)
	if adminID != nil {
		s.log.WarnContext(ctx, "admin ord report rebuild", logx.KeyOp, op, "period", period.String(), "admin_user_id", *adminID)
	}
	return s.Report(ctx, period)
}

// reportPlacements переводит размещения в вход правил: креатив, площадка, договоры, снимки.
func (s *Service) reportPlacements(ctx context.Context, store domain.Reports, period domain.Period,
	source []domain.SourcePlacement) ([]domain.ReportPlacement, error) {
	const op = "Marking.BuildReport"
	startViews, err := store.Snapshots(ctx, period)
	if err != nil {
		return nil, err
	}
	endViews, err := store.Snapshots(ctx, period.Next())
	if err != nil {
		return nil, err
	}
	platformOrg, err := s.Accounts.Platform(ctx)
	if err != nil {
		return nil, fmt.Errorf("platform organization: %w", err)
	}
	out := make([]domain.ReportPlacement, 0, len(source))
	for _, p := range source {
		cr, err := s.Creatives.ByVersion(ctx, p.PostVersionID)
		if errors.Is(err, domain.ErrNotFound) {
			s.log.WarnContext(ctx, "ord report placement without creative", logx.KeyOp, op, "placement_id", p.PlacementID)
			continue
		}
		if err != nil {
			return nil, err
		}
		pl, err := s.Platforms.ByChannel(ctx, p.ChannelID)
		if errors.Is(err, domain.ErrNotFound) {
			s.log.WarnContext(ctx, "ord report placement without platform", logx.KeyOp, op, "placement_id", p.PlacementID)
			continue
		}
		if err != nil {
			return nil, err
		}
		rp := domain.ReportPlacement{
			PlacementID: p.PlacementID, CreativeID: cr.ID, PlatformID: pl.ID, PublishedAt: p.PublishedAt,
			RemovedAt: p.RemovedAt, FinalViews: p.FinalViews, AdvertiserAmount: p.AdvertiserAmount, BloggerAmount: p.BloggerAmount,
		}
		rp.ViewsAtStart, rp.HasStart = startViews[p.PublicationID]
		rp.ViewsAtEnd, rp.HasEnd = endViews[p.PublicationID]
		if rp.InitialContractID, err = s.contractOf(ctx, domain.ContractInitial, platformOrg.ID, p.AdvertiserOrgID); err != nil {
			return nil, err
		}
		if rp.PublisherContractID, err = s.contractOf(ctx, domain.ContractPublisher, platformOrg.ID, p.BloggerOrgID); err != nil {
			return nil, err
		}
		out = append(out, rp)
	}
	return out, nil
}

// contractOf — договор вида kind с организацией other; uuid.Nil — его нет (акт не формируется).
func (s *Service) contractOf(ctx context.Context, kind domain.ContractKind, platformOrg, other uuid.UUID) (uuid.UUID, error) {
	client, contractor := domain.ContractParties(kind, platformOrg, other)
	c, err := s.Contracts.ByParties(ctx, kind, client, contractor)
	if errors.Is(err, domain.ErrNotFound) {
		return uuid.Nil, nil
	}
	return c.ID, err
}

func (s *Service) saveStatistics(ctx context.Context, store domain.Reports, period domain.Period, lines []domain.StatisticsLine,
	created, changed, unchanged, removed *int) error {
	const op = "Marking.BuildReport"
	at := s.Clock.Now()
	existing, err := store.Statistics(ctx, period)
	if err != nil {
		return err
	}
	byPair := map[[2]uuid.UUID]domain.Statistics{}
	for _, st := range existing {
		byPair[[2]uuid.UUID{st.CreativeID, st.PlatformID}] = st
	}
	seen := map[[2]uuid.UUID]bool{}
	for _, l := range lines {
		key := [2]uuid.UUID{l.CreativeID, l.PlatformID}
		seen[key] = true
		cur, ok := byPair[key]
		if !ok {
			st := domain.Statistics{ID: ids.New(), CreativeID: l.CreativeID, PlatformID: l.PlatformID, PlacementID: l.PlacementID,
				Period: period, Sync: domain.NewSyncState(), CreatedAt: at, UpdatedAt: at}
			st.SetLine(l)
			if err := store.InsertStatistics(ctx, st); err != nil {
				return err
			}
			*created++
			continue
		}
		locked, err := store.StatisticsForUpdate(ctx, cur.ID)
		if err != nil {
			return err
		}
		if !locked.SetLine(l) {
			*unchanged++
			s.log.DebugContext(ctx, "ord report object unchanged", logx.KeyOp, op, KeyObjectType, domain.TypeStatistics, KeyObjectID, locked.ID)
			continue
		}
		locked.Sync.Invalidate() // R-21: правка после спора — запись снова ждёт отправки
		locked.UpdatedAt = at
		if err := store.SaveStatistics(ctx, locked); err != nil {
			return err
		}
		*changed++
	}
	// Пара больше не в отчёте: неотправленная удаляется, отправленная обнуляется и ждёт отправки.
	for key, cur := range byPair {
		if seen[key] {
			continue
		}
		if cur.Sync.ExternalID == "" && cur.Sync.Status != domain.SyncSent {
			if err := store.DeleteStatistics(ctx, cur.ID); err != nil {
				return err
			}
			*removed++
			continue
		}
		locked, err := store.StatisticsForUpdate(ctx, cur.ID)
		if err != nil {
			return err
		}
		if locked.SetLine(domain.StatisticsLine{ShownFrom: locked.ShownFrom, ShownTo: locked.ShownTo}) {
			locked.Sync.Invalidate()
			locked.UpdatedAt = at
			if err := store.SaveStatistics(ctx, locked); err != nil {
				return err
			}
			*removed++
		}
	}
	return nil
}

func (s *Service) saveInvoices(ctx context.Context, store domain.Reports, period domain.Period, drafts []domain.InvoiceDraft) (int, error) {
	const op = "Marking.BuildReport"
	at := s.Clock.Now()
	existing, err := store.Invoices(ctx, period)
	if err != nil {
		return 0, err
	}
	byContract := map[uuid.UUID]domain.Invoice{}
	for _, inv := range existing {
		byContract[inv.ContractID] = inv
	}
	seq := len(existing)
	seen := map[uuid.UUID]bool{}
	rules := s.Config.Report
	for _, d := range drafts {
		seen[d.ContractID] = true
		cur, ok := byContract[d.ContractID]
		if !ok {
			seq++
			inv := domain.Invoice{ID: ids.New(), ContractID: d.ContractID, Period: period, Type: rules.InvoiceType(d.Kind),
				Number: domain.InvoiceNumber(rules.NumberPrefix, period, seq), InvoiceDate: period.LastDay(), Amount: d.Amount,
				Items: d.Items, Sync: domain.NewSyncState(), CreatedAt: at, UpdatedAt: at}
			if err := store.InsertInvoice(ctx, inv); err != nil {
				return 0, err
			}
			continue
		}
		locked, err := store.InvoiceForUpdate(ctx, cur.ID)
		if err != nil {
			return 0, err
		}
		if locked.Amount == d.Amount && sameItems(locked.Items, d.Items) && locked.Type == rules.InvoiceType(d.Kind) {
			s.log.DebugContext(ctx, "ord report object unchanged", logx.KeyOp, op, KeyObjectType, domain.TypeInvoice, KeyObjectID, locked.ID)
			continue
		}
		locked.Amount, locked.Items, locked.Type, locked.UpdatedAt = d.Amount, d.Items, rules.InvoiceType(d.Kind), at
		locked.Sync.Invalidate()
		if err := store.SaveInvoice(ctx, locked); err != nil {
			return 0, err
		}
	}
	for contract, cur := range byContract {
		if seen[contract] {
			continue
		}
		if cur.Sync.ExternalID == "" && cur.Sync.Status != domain.SyncSent {
			if err := store.DeleteInvoice(ctx, cur.ID); err != nil {
				return 0, err
			}
			continue
		}
		// Отправленный акт без суммы ОРД не примет (amount > 0): правка — вручную в кабинете ОРД.
		s.log.WarnContext(ctx, "ord report invoice left without amount", logx.KeyOp, op, KeyObjectID, cur.ID, "period", period.String())
	}
	return len(drafts), nil
}

func sameItems(a, b []domain.InvoiceItem) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[[2]uuid.UUID]int64{}
	for _, it := range a {
		m[[2]uuid.UUID{it.CreativeID, it.PlatformID}] = it.Amount
	}
	for _, it := range b {
		if v, ok := m[[2]uuid.UUID{it.CreativeID, it.PlatformID}]; !ok || v != it.Amount {
			return false
		}
	}
	return true
}

// SendReport ставит в очередь отправки записи месяца в pending и error (R-21). Отчёт не
// сформирован — 409 report_not_built. adminID — ручная отправка (журнал действий).
func (s *Service) SendReport(ctx context.Context, period domain.Period, adminID *uuid.UUID, reason string) (*ReportView, error) {
	const op = "Marking.SendReport"
	store, err := s.reports()
	if err != nil {
		return nil, err
	}
	if !period.Closed(s.Clock.Now()) {
		return nil, apperr.Validation("period", domain.ReasonPeriodNotClosed).WithDetail("reason", domain.ReasonPeriodNotClosed)
	}
	if adminID != nil {
		ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: adminID})
	}
	queued := 0
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		state, err := store.Report(ctx, period)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.Conflict("ord report is not built").WithDetail("reason", ReasonReportNotBuilt)
		}
		if err != nil {
			return err
		}
		statsList, err := store.Statistics(ctx, period)
		if err != nil {
			return err
		}
		invoices, err := store.Invoices(ctx, period)
		if err != nil {
			return err
		}
		refs := make([]domain.ObjectRef, 0, len(statsList)+len(invoices))
		for _, st := range statsList {
			refs = append(refs, st.Ref())
		}
		for _, inv := range invoices {
			refs = append(refs, inv.Ref())
		}
		now := s.Clock.Now()
		for _, ref := range refs {
			r, err := s.lock(ctx, ref)
			if err != nil {
				return err
			}
			st := r.state()
			switch st.Status {
			case domain.SyncError:
				if err := st.Retry(); err != nil {
					return err
				}
				if err := r.save(ctx, s, now); err != nil {
					return err
				}
			case domain.SyncPending:
			default:
				continue
			}
			if err := s.enqueueSync(ctx, op, ref, reasonNew); err != nil {
				return err
			}
			queued++
		}
		state.SendRequestedAt, state.SendRequestedBy, state.UpdatedAt = &now, adminID, now
		if err := store.SaveReport(ctx, state); err != nil {
			return err
		}
		if adminID == nil {
			return nil
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionReportSend, ObjectType: "ord_report", ObjectID: period.String(), ActorType: audit.ActorAdmin,
			Reason: &reason, Data: map[string]any{"objects": queued},
		})
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "ord report send requested", logx.KeyOp, op, "period", period.String(), "objects", queued,
		"manual", adminID != nil)
	if adminID != nil {
		s.log.WarnContext(ctx, "admin ord report send", logx.KeyOp, op, "period", period.String(), "admin_user_id", *adminID)
	}
	return s.Report(ctx, period)
}

// Report — отчёт месяца для экрана; не формировался — not_built и пустые списки.
func (s *Service) Report(ctx context.Context, period domain.Period) (*ReportView, error) {
	store, err := s.reports()
	if err != nil {
		return nil, err
	}
	v := &ReportView{Period: period, State: ReportNotBuilt}
	state, err := store.Report(ctx, period)
	if errors.Is(err, domain.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	v.Built = &state
	statsList, err := store.Statistics(ctx, period)
	if err != nil {
		return nil, err
	}
	invoices, err := store.Invoices(ctx, period)
	if err != nil {
		return nil, err
	}
	count := func(st domain.SyncState) {
		switch st.Status {
		case domain.SyncError:
			v.ErrorCount++
		case domain.SyncPending, domain.SyncSent:
			v.PendingCount++
		}
	}
	for _, st := range statsList {
		row := ReportStatisticsRow{Statistics: st}
		if cr, err := s.Creatives.GetForUpdate(ctx, st.CreativeID); err == nil {
			row.Erid = cr.Erid
		}
		if pl, err := s.Platforms.GetForUpdate(ctx, st.PlatformID); err == nil {
			row.ChannelID, row.Channel = pl.ChannelID, pl.Name
		}
		v.Statistics = append(v.Statistics, row)
		v.Impressions += st.Impressions
		v.Amount += st.Amount
		count(st.Sync)
	}
	for _, inv := range invoices {
		row := ReportInvoiceRow{Invoice: inv}
		if c, err := s.Contracts.GetForUpdate(ctx, inv.ContractID); err == nil {
			row.Kind = c.Kind
			row.OrganizationID = c.ClientOrganizationID
			if c.Kind == domain.ContractPublisher {
				row.OrganizationID = c.ContractorOrganizationID
			}
		}
		v.Invoices = append(v.Invoices, row)
		count(inv.Sync)
	}
	v.StatisticsCount, v.InvoicesCount = len(v.Statistics), len(v.Invoices)
	switch {
	case v.ErrorCount > 0:
		v.State = ReportHasErrors
	case v.PendingCount == 0 && (state.SendRequestedAt != nil || v.StatisticsCount+v.InvoicesCount == 0):
		v.State = ReportSent
	case state.SendRequestedAt != nil:
		v.State = ReportSending
	default:
		v.State = ReportBuilt
	}
	return v, nil
}

// ListReports — сформированные месяцы, новые первыми.
func (s *Service) ListReports(ctx context.Context) ([]*ReportView, error) {
	store, err := s.reports()
	if err != nil {
		return nil, err
	}
	list, err := store.ReportList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*ReportView, 0, len(list))
	for _, st := range list {
		v, err := s.Report(ctx, st.Period)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// PendingReport — прошедший месяц ещё не сдан: не сформирован или есть неподтверждённые записи
// (счётчик меню администратора, R-16).
func (s *Service) PendingReport(ctx context.Context) (bool, error) {
	if s.Reports == nil || s.Source == nil {
		return false, nil
	}
	v, err := s.Report(ctx, domain.PeriodOf(s.Clock.Now()).Prev())
	if err != nil {
		return false, err
	}
	return v.State != ReportSent, nil
}

// CheckReport — ежедневная проверка (R-23): с ORD_REPORT_ERROR_DAY числа прошедший месяц не сдан —
// ERROR и оповещение ord_report_overdue:{period}.
func (s *Service) CheckReport(ctx context.Context) error {
	const op = "Marking.CheckReport"
	now := s.Clock.Now()
	period := domain.PeriodOf(now).Prev()
	if now.In(period.End().Location()).Day() < s.Config.Report.ErrorDay {
		return nil
	}
	v, err := s.Report(ctx, period)
	if err != nil {
		return err
	}
	if v.State == ReportSent {
		s.log.DebugContext(ctx, "ord report delivered", logx.KeyOp, op, "period", period.String())
		return nil
	}
	s.log.ErrorContext(ctx, "ord report overdue", logx.KeyOp, op, "period", period.String(), "state", v.State,
		"pending", v.PendingCount, "errors", v.ErrorCount, "due_date", period.DueDate().Format("2006-01-02"))
	s.raise(ctx, op, alert.Alert{
		Reason: alertReportDue + ":" + period.String(), Level: alert.LevelError,
		Summary: fmt.Sprintf("ORD report for %s is not delivered (%s), due %s", period, v.State, period.DueDate().Format("2006-01-02")),
		Fields:  map[string]any{"period": period.String(), "state": string(v.State), "pending": v.PendingCount, "errors": v.ErrorCount},
	})
	return nil
}

// MonthlyReport — задание 1-го числа (R-18): снимок начала месяца, формирование прошедшего,
// отправка при ORD_REPORT_AUTO_SEND.
func (s *Service) MonthlyReport(ctx context.Context) error {
	current := domain.PeriodOf(s.Clock.Now())
	if _, err := s.TakeSnapshot(ctx, current); err != nil {
		return err
	}
	prev := current.Prev()
	if _, err := s.BuildReport(ctx, prev, nil, ""); err != nil {
		return err
	}
	if !s.Config.Report.AutoSend {
		return nil
	}
	_, err := s.SendReport(ctx, prev, nil, "")
	return err
}

// ---------------------------------------------------------------- сборка запросов (sync_ord_object)

// buildStatistics — запрос статистики: креатив с erid и площадка готовы (R-20).
func (s *Service) buildStatistics(ctx context.Context, r *statisticsRecord) (*outgoing, error, error) {
	const op = "Marking.Sync"
	cr, err := s.Creatives.GetForUpdate(ctx, r.v.CreativeID)
	if err != nil {
		return nil, nil, err
	}
	pl, err := s.Platforms.GetForUpdate(ctx, r.v.PlatformID)
	if err != nil {
		return nil, nil, err
	}
	v := domain.DependencyVerdict([]domain.Dependency{creativeDependency(cr), dependency(domain.NeedPlatform, pl.Ref(), pl.Sync)})
	if !v.Ready() {
		return nil, s.wait(ctx, op, r.ref(), v), nil
	}
	req := domain.NewStatisticsRequest(r.v, cr.ExternalID(), pl.ExternalID())
	hash, err := domain.PayloadHash(req)
	if err != nil {
		return nil, nil, err
	}
	return &outgoing{ref: r.ref(), externalID: r.externalID(), hash: hash,
		call: func(ctx context.Context, api domain.OrdAPI) (domain.Receipt, error) {
			return api.UpsertStatistics(ctx, req)
		},
	}, nil, nil
}

// buildInvoice — запрос акта: договор, креативы разбивки и площадки готовы (R-22).
func (s *Service) buildInvoice(ctx context.Context, r *invoiceRecord) (*outgoing, error, error) {
	const op = "Marking.Sync"
	ct, err := s.Contracts.GetForUpdate(ctx, r.v.ContractID)
	if err != nil {
		return nil, nil, err
	}
	deps := []domain.Dependency{dependency(domain.NeedContract, ct.Ref(), ct.Sync)}
	items := make([]domain.InvoiceRequestItem, 0, len(r.v.Items))
	for _, it := range r.v.Items {
		cr, err := s.Creatives.GetForUpdate(ctx, it.CreativeID)
		if err != nil {
			return nil, nil, err
		}
		pl, err := s.Platforms.GetForUpdate(ctx, it.PlatformID)
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, creativeDependency(cr), dependency(domain.NeedPlatform, pl.Ref(), pl.Sync))
		items = append(items, domain.InvoiceRequestItem{CreativeID: cr.ExternalID(), PlatformID: pl.ExternalID(), Amount: domain.Rubles(it.Amount)})
	}
	if v := domain.DependencyVerdict(deps); !v.Ready() {
		return nil, s.wait(ctx, op, r.ref(), v), nil
	}
	req := domain.NewInvoiceRequest(r.v, ct.ExternalID(), items)
	hash, err := domain.PayloadHash(req)
	if err != nil {
		return nil, nil, err
	}
	return &outgoing{ref: r.ref(), externalID: r.externalID(), hash: hash,
		call: func(ctx context.Context, api domain.OrdAPI) (domain.Receipt, error) {
			return api.UpsertInvoice(ctx, req)
		},
	}, nil, nil
}

// creativeDependency — креатив готов для отчётности, когда у него есть erid.
func creativeDependency(cr domain.Creative) domain.Dependency {
	d := dependency(domain.NeedCreative, cr.Ref(), cr.Sync)
	if !cr.HasErid() && d.State.Status != domain.SyncError {
		d.State.Status = domain.SyncPending
	}
	return d
}
