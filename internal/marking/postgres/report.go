package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/marking/postgres/sqlc"
)

// Reports — ежемесячная отчётность ОРД: снимки, статистика, акты, состояние месяца (R-18 … R-24).
type Reports struct{ *Store }

// Reports возвращает репозиторий отчётности.
func (s *Store) Reports() *Reports { return &Reports{s} }

var _ domain.Reports = (*Reports)(nil)

func (r *Reports) InsertSnapshots(ctx context.Context, period domain.Period, views []domain.PublicationViews, now time.Time) (int, error) {
	q, n := r.q(ctx), 0
	for _, v := range views {
		added, err := q.InsertViewsSnapshot(ctx, sqlc.InsertViewsSnapshotParams{
			PublicationID: v.PublicationID, Period: pgDate(period.Date()), Views: int32(v.Views),
			ViewsCheckedAt: v.CheckedAt, CreatedAt: now,
		})
		if err != nil {
			return n, r.fail(ctx, "OrdReports.InsertSnapshots", err)
		}
		n += int(added)
	}
	return n, nil
}

func (r *Reports) Snapshots(ctx context.Context, period domain.Period) (map[uuid.UUID]int64, error) {
	rows, err := r.q(ctx).ListViewsSnapshots(ctx, pgDate(period.Date()))
	if err != nil {
		return nil, r.fail(ctx, "OrdReports.Snapshots", err)
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		out[row.PublicationID] = int64(row.Views)
	}
	return out, nil
}

func statisticsFromRow(r sqlc.OrdStatistic) domain.Statistics {
	return domain.Statistics{
		ID: r.ID, CreativeID: r.CreativeID, PlatformID: r.OrdPlatformID, PlacementID: r.PlacementID,
		Period: domain.PeriodFromDate(dateVal(r.Period)), Impressions: r.Impressions,
		ShownFrom: dateVal(r.ShownFrom), ShownTo: dateVal(r.ShownTo), Amount: r.Amount,
		Sync: fromCols(syncCols{
			ExternalID: r.OrdExternalID, RequestID: r.SyncRequestID, Error: r.SyncError, PayloadHash: r.PayloadHash,
			OrdStatus: r.OrdStatus, Status: r.SyncStatus, Attempts: r.SyncAttempts, SyncedAt: r.SyncedAt, SentAt: r.SentAt,
		}),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func (r *Reports) Statistics(ctx context.Context, period domain.Period) ([]domain.Statistics, error) {
	rows, err := r.q(ctx).ListOrdStatistics(ctx, pgDate(period.Date()))
	if err != nil {
		return nil, r.fail(ctx, "OrdReports.Statistics", err)
	}
	out := make([]domain.Statistics, 0, len(rows))
	for _, row := range rows {
		out = append(out, statisticsFromRow(row))
	}
	return out, nil
}

func (r *Reports) StatisticsForUpdate(ctx context.Context, id uuid.UUID) (domain.Statistics, error) {
	row, err := r.q(ctx).GetOrdStatisticsForUpdate(ctx, id)
	if err != nil {
		return domain.Statistics{}, r.fail(ctx, "OrdReports.StatisticsForUpdate", err)
	}
	return statisticsFromRow(row), nil
}

func (r *Reports) InsertStatistics(ctx context.Context, s domain.Statistics) error {
	c := toCols(s.Sync)
	if err := r.q(ctx).InsertOrdStatistics(ctx, sqlc.InsertOrdStatisticsParams{
		ID: s.ID, CreativeID: s.CreativeID, OrdPlatformID: s.PlatformID, PlacementID: s.PlacementID,
		Period: pgDate(s.Period.Date()), Impressions: s.Impressions, ShownFrom: pgDate(s.ShownFrom), ShownTo: pgDate(s.ShownTo),
		Amount: s.Amount, OrdExternalID: c.ExternalID, SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error,
		SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdReports.InsertStatistics", err)
	}
	r.saved(ctx, "OrdReports.InsertStatistics", domain.TypeStatistics, s.ID, s.Sync.Status)
	return nil
}

func (r *Reports) SaveStatistics(ctx context.Context, s domain.Statistics) error {
	c := toCols(s.Sync)
	if err := r.q(ctx).SaveOrdStatistics(ctx, sqlc.SaveOrdStatisticsParams{
		ID: s.ID, Impressions: s.Impressions, ShownFrom: pgDate(s.ShownFrom), ShownTo: pgDate(s.ShownTo), Amount: s.Amount,
		OrdExternalID: c.ExternalID, SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error,
		SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt,
		UpdatedAt: s.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdReports.SaveStatistics", err)
	}
	r.saved(ctx, "OrdReports.SaveStatistics", domain.TypeStatistics, s.ID, s.Sync.Status)
	return nil
}

func (r *Reports) DeleteStatistics(ctx context.Context, id uuid.UUID) error {
	if err := r.q(ctx).DeleteOrdStatistics(ctx, id); err != nil {
		return r.fail(ctx, "OrdReports.DeleteStatistics", err)
	}
	return nil
}

func invoiceFromRow(r sqlc.OrdInvoice) domain.Invoice {
	return domain.Invoice{
		ID: r.ID, ContractID: r.OrdContractID, Period: domain.PeriodFromDate(dateVal(r.Period)), Type: r.Type,
		Number: r.Number, InvoiceDate: dateVal(r.InvoiceDate), Amount: r.Amount,
		Sync: fromCols(syncCols{
			ExternalID: r.OrdExternalID, RequestID: r.SyncRequestID, Error: r.SyncError, PayloadHash: r.PayloadHash,
			OrdStatus: r.OrdStatus, Status: r.SyncStatus, Attempts: r.SyncAttempts, SyncedAt: r.SyncedAt, SentAt: r.SentAt,
		}),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func itemFromRow(r sqlc.OrdInvoiceItem) domain.InvoiceItem {
	return domain.InvoiceItem{CreativeID: r.CreativeID, PlatformID: r.OrdPlatformID, Amount: r.Amount}
}

func (r *Reports) Invoices(ctx context.Context, period domain.Period) ([]domain.Invoice, error) {
	q := r.q(ctx)
	rows, err := q.ListOrdInvoices(ctx, pgDate(period.Date()))
	if err != nil {
		return nil, r.fail(ctx, "OrdReports.Invoices", err)
	}
	items, err := q.ListOrdInvoiceItems(ctx, pgDate(period.Date()))
	if err != nil {
		return nil, r.fail(ctx, "OrdReports.Invoices", err)
	}
	byInvoice := map[uuid.UUID][]domain.InvoiceItem{}
	for _, it := range items {
		byInvoice[it.OrdInvoiceID] = append(byInvoice[it.OrdInvoiceID], itemFromRow(it))
	}
	out := make([]domain.Invoice, 0, len(rows))
	for _, row := range rows {
		inv := invoiceFromRow(row)
		inv.Items = byInvoice[inv.ID]
		out = append(out, inv)
	}
	return out, nil
}

func (r *Reports) InvoiceForUpdate(ctx context.Context, id uuid.UUID) (domain.Invoice, error) {
	q := r.q(ctx)
	row, err := q.GetOrdInvoiceForUpdate(ctx, id)
	if err != nil {
		return domain.Invoice{}, r.fail(ctx, "OrdReports.InvoiceForUpdate", err)
	}
	inv := invoiceFromRow(row)
	items, err := q.ListOrdInvoiceItemsOf(ctx, id)
	if err != nil {
		return domain.Invoice{}, r.fail(ctx, "OrdReports.InvoiceForUpdate", err)
	}
	for _, it := range items {
		inv.Items = append(inv.Items, itemFromRow(it))
	}
	return inv, nil
}

func (r *Reports) InsertInvoice(ctx context.Context, i domain.Invoice) error {
	c := toCols(i.Sync)
	if err := r.q(ctx).InsertOrdInvoice(ctx, sqlc.InsertOrdInvoiceParams{
		ID: i.ID, OrdContractID: i.ContractID, Period: pgDate(i.Period.Date()), Type: i.Type, Number: i.Number,
		InvoiceDate: pgDate(i.InvoiceDate), Amount: i.Amount, OrdExternalID: c.ExternalID, SyncStatus: c.Status,
		SyncRequestID: c.RequestID, SyncError: c.Error, SyncAttempts: c.Attempts, SyncedAt: c.SyncedAt,
		PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdReports.InsertInvoice", err)
	}
	if err := r.replaceItems(ctx, i); err != nil {
		return err
	}
	r.saved(ctx, "OrdReports.InsertInvoice", domain.TypeInvoice, i.ID, i.Sync.Status)
	return nil
}

func (r *Reports) SaveInvoice(ctx context.Context, i domain.Invoice) error {
	c := toCols(i.Sync)
	if err := r.q(ctx).SaveOrdInvoice(ctx, sqlc.SaveOrdInvoiceParams{
		ID: i.ID, Type: i.Type, InvoiceDate: pgDate(i.InvoiceDate), Amount: i.Amount, OrdExternalID: c.ExternalID,
		SyncStatus: c.Status, SyncRequestID: c.RequestID, SyncError: c.Error, SyncAttempts: c.Attempts,
		SyncedAt: c.SyncedAt, PayloadHash: c.PayloadHash, OrdStatus: c.OrdStatus, SentAt: c.SentAt, UpdatedAt: i.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdReports.SaveInvoice", err)
	}
	if err := r.replaceItems(ctx, i); err != nil {
		return err
	}
	r.saved(ctx, "OrdReports.SaveInvoice", domain.TypeInvoice, i.ID, i.Sync.Status)
	return nil
}

func (r *Reports) replaceItems(ctx context.Context, i domain.Invoice) error {
	q := r.q(ctx)
	if err := q.DeleteOrdInvoiceItems(ctx, i.ID); err != nil {
		return r.fail(ctx, "OrdReports.replaceItems", err)
	}
	for _, it := range i.Items {
		if err := q.InsertOrdInvoiceItem(ctx, sqlc.InsertOrdInvoiceItemParams{
			OrdInvoiceID: i.ID, CreativeID: it.CreativeID, OrdPlatformID: it.PlatformID, Amount: it.Amount,
		}); err != nil {
			return r.fail(ctx, "OrdReports.replaceItems", err)
		}
	}
	return nil
}

func (r *Reports) DeleteInvoice(ctx context.Context, id uuid.UUID) error {
	q := r.q(ctx)
	if err := q.DeleteOrdInvoiceItems(ctx, id); err != nil {
		return r.fail(ctx, "OrdReports.DeleteInvoice", err)
	}
	if err := q.DeleteOrdInvoice(ctx, id); err != nil {
		return r.fail(ctx, "OrdReports.DeleteInvoice", err)
	}
	return nil
}

func reportFromRow(r sqlc.OrdReport) domain.ReportState {
	return domain.ReportState{
		Period: domain.PeriodFromDate(dateVal(r.Period)), BuiltAt: r.BuiltAt.UTC(), BuiltBy: r.BuiltBy,
		SendRequestedAt: r.SendRequestedAt, SendRequestedBy: r.SendRequestedBy, UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func (r *Reports) Report(ctx context.Context, period domain.Period) (domain.ReportState, error) {
	row, err := r.q(ctx).GetOrdReport(ctx, pgDate(period.Date()))
	if err != nil {
		return domain.ReportState{}, r.fail(ctx, "OrdReports.Report", err)
	}
	return reportFromRow(row), nil
}

func (r *Reports) SaveReport(ctx context.Context, s domain.ReportState) error {
	if err := r.q(ctx).UpsertOrdReport(ctx, sqlc.UpsertOrdReportParams{
		Period: pgDate(s.Period.Date()), BuiltAt: s.BuiltAt, BuiltBy: s.BuiltBy, SendRequestedAt: s.SendRequestedAt,
		SendRequestedBy: s.SendRequestedBy, UpdatedAt: s.UpdatedAt,
	}); err != nil {
		return r.fail(ctx, "OrdReports.SaveReport", err)
	}
	return nil
}

func (r *Reports) ReportList(ctx context.Context) ([]domain.ReportState, error) {
	rows, err := r.q(ctx).ListOrdReports(ctx)
	if err != nil {
		return nil, r.fail(ctx, "OrdReports.ReportList", err)
	}
	out := make([]domain.ReportState, 0, len(rows))
	for _, row := range rows {
		out = append(out, reportFromRow(row))
	}
	return out, nil
}
