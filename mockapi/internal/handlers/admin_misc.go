package handlers

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"maxim/mockapi/internal/api"
	"maxim/mockapi/internal/store"
)

// Организации и пользователи (S-20), журнал действий, ОРД (S-20, S-31), счётчики меню.

// ---------------------------------------------------------------- организации

func orgItem(viewer *store.User, o *store.Organization) api.AdminOrganizationListItem {
	return api.AdminOrganizationListItem{Id: o.ID, Name: o.Name, Inn: innFor(viewer, o.INN), LegalType: api.LegalType(o.LegalType),
		Status: api.AdminOrganizationListItemStatus(o.Status), Cabinets: cabinets(o), CreatedAt: o.CreatedAt}
}

func adminUser(viewer, u *store.User) api.AdminUser {
	out := api.AdminUser{Id: u.ID, Email: emailFor(viewer, u.Email), Status: api.AdminUserStatusActive,
		BlockedReason: strPtr(u.BlockedReason), BlockedAt: u.BlockedAt, LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt}
	if u.Blocked {
		out.Status = api.AdminUserStatusBlocked
	}
	if u.AdminRole != "" {
		out.AdminRole = ptr(api.AdminRole(u.AdminRole))
	}
	return out
}

func adminOrganization(tx *store.Tx, viewer *store.User, o *store.Organization) api.AdminOrganization {
	out := recast[api.AdminOrganization](orgItem(viewer, o))
	out.Kpp, out.ContactName, out.Address = o.KPP, strPtr(o.ContactName), o.Address
	phone := o.Phone
	if !isSenior(viewer) {
		phone = maskPhone(phone)
	}
	out.Phone = strPtr(phone)
	if o.TaxRegime != nil {
		out.TaxRegime = ptr(api.TaxRegime(*o.TaxRegime))
	}
	if o.VatRate != nil {
		out.VatRate = ptr(api.VatRate(*o.VatRate))
	}
	out.TaxDocumentUploaded = o.TaxDocUploaded
	out.Members = []api.AdminUser{}
	for _, u := range tx.OrgUsers(o.ID) {
		out.Members = append(out.Members, adminUser(viewer, u))
	}
	slices.SortFunc(out.Members, func(a, b api.AdminUser) int { return a.CreatedAt.Compare(b.CreatedAt) })
	if ob, err := tx.OrdObjectByRef("organization", o.ID); err == nil {
		v := ordObject(ob, false)
		out.OrdCounterparty = &v
	}
	return out
}

func (h *Handlers) ListAdminOrganizations(ctx context.Context, req api.ListAdminOrganizationsRequestObject) (api.ListAdminOrganizationsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := admin(ctx, tx)
	if err != nil {
		return nil, err
	}
	q := req.Params
	var found []*store.Organization
	for _, o := range tx.D.Orgs {
		if q.Role != nil && !slices.Contains(cabinets(o), *q.Role) {
			continue
		}
		if q.Query != nil {
			s := strings.ToLower(*q.Query)
			// ИНН: senior — по началу, moderator — только целиком (под маской подбор по началу запрещён).
			innHit := o.INN == s || (isSenior(u) && strings.HasPrefix(o.INN, s))
			hit := innHit || strings.Contains(strings.ToLower(o.Name), s)
			for _, m := range tx.OrgUsers(o.ID) {
				hit = hit || strings.Contains(m.Email, s)
			}
			if !hit {
				continue
			}
		}
		found = append(found, o)
	}
	found = newestFirst(found, func(o *store.Organization) time.Time { return o.CreatedAt })
	pg, next, total := page(found, q.Limit, q.Cursor)
	items := []api.AdminOrganizationListItem{}
	for _, o := range pg {
		items = append(items, orgItem(u, o))
	}
	return api.ListAdminOrganizations200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) GetAdminOrganization(ctx context.Context, req api.GetAdminOrganizationRequestObject) (api.GetAdminOrganizationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := admin(ctx, tx)
	if err != nil {
		return nil, err
	}
	o, err := tx.Org(req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetAdminOrganization200JSONResponse(adminOrganization(tx, u, o)), nil
}

var kppRe = regexp.MustCompile(`^[0-9]{9}$`)

func (h *Handlers) UpdateAdminOrganization(ctx context.Context, req api.UpdateAdminOrganizationRequestObject) (api.UpdateAdminOrganizationResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	o, err := tx.Org(req.Id)
	if err != nil {
		return nil, err
	}
	b := req.Body
	fields := []string{}
	if b.Name != nil {
		if strings.TrimSpace(*b.Name) == "" {
			return nil, store.Validation("name", "name is empty")
		}
		o.Name = strings.TrimSpace(*b.Name)
		fields = append(fields, "name")
	}
	if b.Kpp != nil {
		if o.LegalType != "legal_entity" || !kppRe.MatchString(*b.Kpp) {
			return nil, store.Validation("kpp", "kpp is allowed for legal_entity only, 9 digits")
		}
		o.KPP = ptr(*b.Kpp)
		fields = append(fields, "kpp")
	}
	if ob, err := tx.OrdObjectByRef("organization", o.ID); err == nil && len(fields) > 0 {
		ob.Status, ob.Error, ob.UpdatedAt = "pending", "", tx.Now
	}
	id := o.ID
	tx.Audit(u.ID, &id, "organization.admin_updated", "organization", o.ID.String(), b.Reason, map[string]any{"fields": fields})
	h.logger(ctx).Warn("admin organization updated", "op", "Admin.UpdateOrganization", "organization_id", o.ID, "fields", fields)
	return api.UpdateAdminOrganization200JSONResponse(adminOrganization(tx, u, o)), nil
}

func targetUser(tx *store.Tx, id uuid.UUID) (*store.User, error) {
	if u, ok := tx.D.Users[id]; ok {
		return u, nil
	}
	return nil, store.NotFound("user", id)
}

func (h *Handlers) BlockUser(ctx context.Context, req api.BlockUserRequestObject) (api.BlockUserResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	a, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	u, err := targetUser(tx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.BlockUser(a.ID, u, req.Body.Reason); err != nil {
		return nil, err
	}
	h.logger(ctx).Warn("user blocked", "op", "Admin.BlockUser", "user_id", u.ID, "admin_user_id", a.ID)
	return api.BlockUser200JSONResponse(adminUser(a, u)), nil
}

func (h *Handlers) UnblockUser(ctx context.Context, req api.UnblockUserRequestObject) (api.UnblockUserResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	a, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	u, err := targetUser(tx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := tx.UnblockUser(a.ID, u, req.Body.Reason); err != nil {
		return nil, err
	}
	return api.UnblockUser200JSONResponse(adminUser(a, u)), nil
}

// ---------------------------------------------------------------- журнал действий

func (h *Handlers) ListAuditLog(ctx context.Context, req api.ListAuditLogRequestObject) (api.ListAuditLogResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := senior(ctx, tx); err != nil {
		return nil, err
	}
	q := req.Params
	var found []*store.AuditEntry
	for _, e := range tx.D.Audit {
		switch {
		case q.ActorUserId != nil && (e.ActorID == nil || *e.ActorID != *q.ActorUserId),
			q.ActorType != nil && string(*q.ActorType) != e.ActorType,
			q.ManualOnly != nil && *q.ManualOnly && e.ActorType != "admin",
			q.Action != nil && *q.Action != e.Action,
			q.ObjectType != nil && *q.ObjectType != e.ObjectType,
			q.ObjectId != nil && *q.ObjectId != e.ObjectID,
			q.OrganizationId != nil && (e.OrgID == nil || *e.OrgID != *q.OrganizationId),
			!inPeriod(e.CreatedAt, q.From, q.To):
			continue
		}
		found = append(found, e)
	}
	found = newestFirst(found, func(e *store.AuditEntry) time.Time { return e.CreatedAt })
	pg, next, total := page(found, q.Limit, q.Cursor)
	items := []api.AuditLogEntry{}
	for _, e := range pg {
		items = append(items, api.AuditLogEntry{Id: e.ID, ActorType: api.AuditLogEntryActorType(e.ActorType),
			Actor: userRefPtr(tx, e.ActorID), OrganizationId: e.OrgID, Action: e.Action, ObjectType: e.ObjectType,
			ObjectId: e.ObjectID, Reason: strPtr(e.Reason), Data: e.Data, CreatedAt: e.CreatedAt})
	}
	return api.ListAuditLog200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

// ---------------------------------------------------------------- ОРД

func ordObject(o *store.OrdObject, full bool) api.OrdObject {
	out := api.OrdObject{Type: api.OrdObjectType(o.Type), Id: o.ID, Title: o.Title, ExternalId: strPtr(o.ExternalID),
		Status: api.OrdSyncStatus(o.Status), OrdStatus: strPtr(o.OrdStatus), Erid: strPtr(o.Erid), Error: strPtr(o.Error),
		Attempts: o.Attempts, UpdatedAt: o.UpdatedAt}
	if full && o.Request != nil {
		req, resp := o.Request, o.Response
		out.LastRequest = &api.OrdRequestLog{Id: store.NewID(), Operation: o.Type, Attempt: max(o.Attempts, 1),
			Request: &req, Response: &resp, ResponseStatus: ptr(200), Error: strPtr(o.Error), CreatedAt: o.UpdatedAt}
		out.Blocked = &[]api.OrdObjectRef{}
	}
	return out
}

func (h *Handlers) ListOrdErrors(ctx context.Context, req api.ListOrdErrorsRequestObject) (api.ListOrdErrorsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := admin(ctx, tx); err != nil {
		return nil, err
	}
	q := req.Params
	var found []*store.OrdObject
	for _, o := range tx.D.OrdObjects {
		if o.Status == "error" && (q.Type == nil || string(*q.Type) == o.Type) {
			found = append(found, o)
		}
	}
	found = slices.Clone(found)
	slices.SortStableFunc(found, func(a, b *store.OrdObject) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	pg, next, total := page(found, q.Limit, q.Cursor)
	items := []api.OrdObject{}
	for _, o := range pg {
		items = append(items, ordObject(o, false))
	}
	return api.ListOrdErrors200JSONResponse{Items: items, NextCursor: next, Total: total}, nil
}

func (h *Handlers) GetOrdObject(ctx context.Context, req api.GetOrdObjectRequestObject) (api.GetOrdObjectResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := admin(ctx, tx); err != nil {
		return nil, err
	}
	o, err := tx.OrdObjectByRef(string(req.Type), req.Id)
	if err != nil {
		return nil, err
	}
	return api.GetOrdObject200JSONResponse(ordObject(o, true)), nil
}

func (h *Handlers) RetryOrdObject(ctx context.Context, req api.RetryOrdObjectRequestObject) (api.RetryOrdObjectResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := admin(ctx, tx)
	if err != nil {
		return nil, err
	}
	o, err := tx.OrdObjectByRef(string(req.Type), req.Id)
	if err != nil {
		return nil, err
	}
	if o.Status != "error" {
		return nil, store.ConflictReason("ord_not_error", "ord object is not in error", map[string]any{"status": o.Status})
	}
	o.Status, o.Error, o.Attempts, o.UpdatedAt = "pending", "", 0, tx.Now
	tx.Audit(u.ID, nil, "ord.retry", o.Type, o.ID.String(), req.Body.Reason, nil)
	return api.RetryOrdObject200JSONResponse(ordObject(o, true)), nil
}

var periodRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// reportDue — 30-е число следующего месяца (в феврале — последний день).
func reportDue(period string) openapi_types.Date {
	t, _ := time.Parse("2006-01", period)
	next := t.AddDate(0, 1, 0)
	last := next.AddDate(0, 1, -1).Day()
	return openapi_types.Date{Time: time.Date(next.Year(), next.Month(), min(30, last), 0, 0, 0, 0, time.UTC)}
}

func ordReportSummary(r *store.OrdReport, period string) api.OrdReportSummary {
	out := api.OrdReportSummary{Period: period, State: api.OrdReportState(store.ReportState(r)), DueDate: reportDue(period)}
	if r == nil {
		return out
	}
	out.BuiltAt, out.SendRequestedAt = r.BuiltAt, r.SendRequestedAt
	out.StatisticsCount, out.InvoicesCount = len(r.Statistics), len(r.Invoices)
	var imps, amount int64
	count := func(status string) {
		switch status {
		case "error":
			out.ErrorCount++
		case "pending", "sent":
			out.PendingCount++
		}
	}
	for _, s := range r.Statistics {
		imps += s.Impressions
		amount += s.Amount
		count(s.Status)
	}
	for _, i := range r.Invoices {
		count(i.Status)
	}
	out.Impressions, out.Amount = &imps, &amount
	return out
}

func ordReport(tx *store.Tx, r *store.OrdReport, period string) api.OrdReport {
	out := recast[api.OrdReport](ordReportSummary(r, period))
	out.Statistics, out.Invoices = []api.OrdStatisticsRow{}, []api.OrdInvoiceRow{}
	if r == nil {
		return out
	}
	for _, s := range r.Statistics {
		row := api.OrdStatisticsRow{Id: s.ID, PlacementId: s.PlacementID, CreativeId: s.CreativeID, Erid: strPtr(s.Erid),
			Impressions: s.Impressions, Amount: s.Amount, ShownFrom: dateOf(s.ShownFrom), ShownTo: dateOf(s.ShownTo),
			Status: api.OrdSyncStatus(s.Status), Error: strPtr(s.Error), Attempts: s.Attempts}
		if c, err := tx.Channel(s.ChannelID); err == nil {
			row.Channel = channelRef(c)
		}
		out.Statistics = append(out.Statistics, row)
	}
	for _, i := range r.Invoices {
		out.Invoices = append(out.Invoices, api.OrdInvoiceRow{Id: i.ID, ContractKind: api.OrdInvoiceRowContractKind(i.ContractKind),
			LinksStatus:  api.OrdSyncStatus("pending"),
			Organization: orgRef(tx, i.OrgID), Number: i.Number, InvoiceDate: dateOf(i.InvoiceDate), Amount: i.Amount,
			ItemsCount: i.ItemsCount, Status: api.OrdSyncStatus(i.Status), Error: strPtr(i.Error), Attempts: i.Attempts})
	}
	return out
}

func (h *Handlers) ListOrdReports(ctx context.Context, req api.ListOrdReportsRequestObject) (api.ListOrdReportsResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := admin(ctx, tx); err != nil {
		return nil, err
	}
	items := []api.OrdReportSummary{}
	for _, r := range tx.D.OrdReports {
		items = append(items, ordReportSummary(r, r.Period))
	}
	slices.SortFunc(items, func(a, b api.OrdReportSummary) int { return strings.Compare(b.Period, a.Period) })
	return api.ListOrdReports200JSONResponse{Items: items}, nil
}

// closedPeriod проверяет формат и что месяц прошёл.
func closedPeriod(tx *store.Tx, period string) error {
	if !periodRe.MatchString(period) {
		return store.Validation("period", "period must be YYYY-MM")
	}
	if period >= tx.Now.In(store.MSK).Format("2006-01") {
		return store.Invalid("period", "period_not_closed", "period is not closed yet")
	}
	return nil
}

func (h *Handlers) GetOrdReport(ctx context.Context, req api.GetOrdReportRequestObject) (api.GetOrdReportResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	if _, err := admin(ctx, tx); err != nil {
		return nil, err
	}
	if !periodRe.MatchString(req.Period) {
		return nil, store.Validation("period", "period must be YYYY-MM")
	}
	return api.GetOrdReport200JSONResponse(ordReport(tx, tx.OrdReportFor(req.Period), req.Period)), nil
}

func (h *Handlers) RebuildOrdReport(ctx context.Context, req api.RebuildOrdReportRequestObject) (api.RebuildOrdReportResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := closedPeriod(tx, req.Period); err != nil {
		return nil, err
	}
	r := tx.OrdReportFor(req.Period)
	if r == nil {
		r = &store.OrdReport{Period: req.Period}
		tx.D.OrdReports = append(tx.D.OrdReports, r)
	}
	now := tx.Now
	r.BuiltAt = &now
	tx.Audit(u.ID, nil, "ord_report.built", "ord_report", req.Period, req.Body.Reason, map[string]any{"statistics": len(r.Statistics)})
	return api.RebuildOrdReport200JSONResponse(ordReport(tx, r, req.Period)), nil
}

func (h *Handlers) SendOrdReport(ctx context.Context, req api.SendOrdReportRequestObject) (api.SendOrdReportResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := senior(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !periodRe.MatchString(req.Period) {
		return nil, store.Validation("period", "period must be YYYY-MM")
	}
	r := tx.OrdReportFor(req.Period)
	if r == nil || r.BuiltAt == nil {
		return nil, store.ConflictReason("report_not_built", "report is not built", nil)
	}
	now := tx.Now
	r.SendRequestedAt = &now
	// Мок «отправляет» сразу: ждущие и ошибочные строки становятся synced.
	for _, s := range r.Statistics {
		if s.Status != "synced" {
			s.Status, s.Error, s.Attempts = "synced", "", s.Attempts+1
		}
	}
	for _, i := range r.Invoices {
		if i.Status != "synced" {
			i.Status, i.Error, i.Attempts = "synced", "", i.Attempts+1
		}
	}
	tx.Audit(u.ID, nil, "ord_report.send_requested", "ord_report", req.Period, req.Body.Reason, nil)
	return api.SendOrdReport200JSONResponse(ordReport(tx, r, req.Period)), nil
}

// ---------------------------------------------------------------- счётчики

func (h *Handlers) GetAdminCounters(ctx context.Context, req api.GetAdminCountersRequestObject) (api.GetAdminCountersResponseObject, error) {
	tx, done := h.st.Begin(ctx)
	defer done()
	u, err := admin(ctx, tx)
	if err != nil {
		return nil, err
	}
	tx.ExpireApprovals()
	out := api.GetAdminCounters200JSONResponse{}
	for _, c := range tx.D.Channels {
		if c.Status == "moderation" {
			out.ChannelsModeration++
		}
	}
	for _, o := range tx.D.Orders {
		if o.Status == "moderation" {
			out.OrdersModeration++
		}
	}
	for _, p := range tx.D.Placements {
		if d := p.Dispute; d != nil && d.Resolution == "" {
			out.DisputesOpen++
			if tx.Now.After(d.DeadlineAt) {
				out.DisputesOverdue++
			}
		}
	}
	for _, o := range tx.D.OrdObjects {
		if o.Status == "error" {
			out.OrdErrors++
		}
	}
	for _, t := range tx.D.BankTxs {
		if t.MatchStatus == "unmatched" {
			out.BankTransactionsUnmatched++
		}
	}
	for _, po := range tx.D.Payouts {
		if po.Status == "failed" && tx.LastPayout(po.PlacementID) == po {
			out.PayoutsFailed++
		}
	}
	for _, a := range tx.D.Approvals {
		if a.Status == "pending" {
			out.ApprovalsPending++
		}
	}
	prev := tx.Now.In(store.MSK).AddDate(0, -1, 0).Format("2006-01")
	out.OrdReportPending = store.ReportState(tx.OrdReportFor(prev)) != "sent"
	if !isSenior(u) {
		out.BankTransactionsUnmatched, out.PayoutsFailed, out.ApprovalsPending = 0, 0, 0
	}
	return out, nil
}
