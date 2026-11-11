package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// InvoicePage — страница счетов; NextCursor пуст на последней странице.
type InvoicePage struct {
	Items      []domain.Invoice
	NextCursor string
}

// CreateInvoice выставляет счёт на пополнение баланса (S-06 шаг 1, R-10, R-11): сумма в
// пределах, лимиты частоты, номер из последовательности, снимок плательщика, PDF в хранилище
// до вставки строки (при ошибке вставки файл остаётся сиротой — безвредно).
func (s *Service) CreateInvoice(ctx context.Context, orgID, userID uuid.UUID, amount money.Kopecks, ip *netip.Addr) (domain.Invoice, error) {
	const op = "Billing.CreateInvoice"
	minimum, maximum := domain.MinInvoiceAmount, domain.MaxInvoiceAmount
	controlled := s.BankConfig.DevPaymentsEnabled()
	if controlled {
		minimum = domain.MinControlledInvoiceAmount
	}
	if amount < minimum || amount > maximum {
		return domain.Invoice{}, apperr.Validation("amount", "out_of_range").
			WithDetail("min", int64(minimum)).WithDetail("max", int64(maximum))
	}
	now := s.Clock.Now()
	if err := s.invoiceRateLimit(ctx, op, userID, ip, now); err != nil {
		return domain.Invoice{}, err
	}
	req, err := s.Organizations.Requisites(ctx, orgID)
	if err != nil {
		return domain.Invoice{}, notFound(err, "organization")
	}
	seq, err := s.Invoices.NextNumber(ctx)
	if err != nil {
		return domain.Invoice{}, err
	}
	inv := domain.Invoice{
		ID: ids.New(), Number: domain.FormatNumber(seq), OrganizationID: orgID, Amount: amount,
		Status: domain.InvoiceIssued, ExpiresAt: domain.ExpiresAt(now, s.Config.InvoiceTTL), Payer: req.Payer(),
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
	}
	inv.PDFStorageKey = "invoices/" + inv.ID.String() + ".pdf"
	paymentPurpose := "Пополнение баланса по счёту № " + inv.Number
	if controlled {
		paymentPurpose += ". " + s.BankConfig.PaymentMarker()
	}
	supplier := s.Config.Supplier
	inv.PaymentDetails = &domain.InvoicePaymentDetails{
		Recipient: supplier.Name, INN: supplier.INN, Account: supplier.BankAccount,
		Bank: supplier.BankName, BIK: supplier.BankBIK, CorrAccount: supplier.BankCorrAccount,
		Purpose: paymentPurpose,
	}
	pdf, err := s.Renderer.Invoice(domain.InvoiceDoc{
		PaymentPurpose: paymentPurpose,
		Number:         inv.Number, IssuedAt: now, ExpiresAt: inv.ExpiresAt, Amount: amount, Supplier: s.Config.Supplier,
		Payer: inv.Payer, VATNote: s.Config.VATNote,
	})
	if err != nil {
		return domain.Invoice{}, err
	}
	if err := s.Storage.PutPrivate(ctx, inv.PDFStorageKey, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		return domain.Invoice{}, err
	}
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Invoices.Insert(ctx, inv); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: ActionInvoiceIssued, ObjectType: "invoice", ObjectID: inv.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"number": inv.Number, "amount": int64(amount)},
		})
	})
	if err != nil {
		return domain.Invoice{}, err
	}
	s.log.InfoContext(ctx, "invoice issued", logx.KeyOp, op, KeyInvoiceID, inv.ID, "number", inv.Number,
		logx.KeyOrganizationID, orgID, KeyAmount, int64(amount))
	return inv, nil
}

// invoiceRateLimit — не больше InvoicesPerUserHour счетов пользователя и InvoicesPerIPHour
// с адреса за час (R-11); счёт по журналу действий.
func (s *Service) invoiceRateLimit(ctx context.Context, op string, userID uuid.UUID, ip *netip.Addr, now time.Time) error {
	if s.Limits == nil {
		return nil
	}
	since := now.Add(-time.Hour)
	n, err := s.Limits.CountByUserSince(ctx, ActionInvoiceIssued, userID, since)
	if err != nil {
		return err
	}
	if s.Config.InvoicesPerUserHour > 0 && n >= s.Config.InvoicesPerUserHour {
		s.log.WarnContext(ctx, "invoice rate limited", logx.KeyOp, op, "user_id", userID, "scope", "user")
		return apperr.RateLimited("user", time.Hour)
	}
	if ip == nil {
		return nil
	}
	n, err = s.Limits.CountByIPSince(ctx, ActionInvoiceIssued, *ip, since)
	if err != nil {
		return err
	}
	if s.Config.InvoicesPerIPHour > 0 && n >= s.Config.InvoicesPerIPHour {
		s.log.WarnContext(ctx, "invoice rate limited", logx.KeyOp, op, "user_id", userID, "scope", "ip")
		return apperr.RateLimited("ip", time.Hour)
	}
	return nil
}

// ListInvoices — счета организации, новые сверху.
func (s *Service) ListInvoices(ctx context.Context, orgID uuid.UUID, cursorStr string, limit int) (InvoicePage, error) {
	after, err := cursor(cursorStr)
	if err != nil {
		return InvoicePage{}, err
	}
	limit = pageLimit(limit)
	list, err := s.Invoices.List(ctx, orgID, after, limit+1)
	if err != nil {
		return InvoicePage{}, err
	}
	page := InvoicePage{}
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		page.NextCursor = domain.Cursor{At: last.CreatedAt, ID: last.ID}.Encode()
	}
	page.Items = list
	return page, nil
}

// InvoicePDF — PDF счёта своей организации; вызывающий закрывает поток.
func (s *Service) InvoicePDF(ctx context.Context, orgID, id uuid.UUID) (domain.Invoice, io.ReadCloser, int64, error) {
	inv, err := s.Invoices.Get(ctx, orgID, id)
	if err != nil {
		return domain.Invoice{}, nil, 0, notFound(err, "invoice")
	}
	r, size, err := s.Storage.Get(ctx, inv.PDFStorageKey)
	if err != nil {
		return domain.Invoice{}, nil, 0, err
	}
	return inv, r, size, nil
}

// ExpireInvoices переводит выставленные счета с вышедшим сроком в expired (R-11) пачками;
// занятые другим экземпляром пропускаются. Возвращает число истёкших.
func (s *Service) ExpireInvoices(ctx context.Context) (int, error) {
	const op = "Billing.ExpireInvoices"
	total := 0
	for {
		n := 0
		err := s.Tx.InTx(ctx, func(ctx context.Context) error {
			now := s.Clock.Now()
			due, err := s.Invoices.DueForExpiry(ctx, now, expireBatch)
			if err != nil {
				return err
			}
			for _, inv := range due {
				if err := inv.Expire(now); err != nil {
					return err
				}
				if err := s.Invoices.Update(ctx, inv); err != nil {
					return err
				}
				s.log.InfoContext(ctx, "invoice expired", logx.KeyOp, op, KeyInvoiceID, inv.ID)
			}
			n = len(due)
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < expireBatch {
			break
		}
	}
	s.log.DebugContext(ctx, "invoices expiry finished", logx.KeyOp, op, "count", total)
	return total, nil
}

// CancelInvoice — отмена счёта администратором из issued или expired с основанием (R-11).
func (s *Service) CancelInvoice(ctx context.Context, id, adminID uuid.UUID, reason string) (domain.Invoice, error) {
	const op = "Billing.CancelInvoice"
	if reason == "" {
		return domain.Invoice{}, apperr.Validation("reason", "required")
	}
	var inv domain.Invoice
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if inv, err = s.Invoices.GetForUpdate(ctx, id); err != nil {
			return notFound(err, "invoice")
		}
		before := inv.Status
		if err := inv.Cancel(reason, s.Clock.Now()); err != nil {
			if errors.Is(err, domain.ErrInvalidTransition) {
				return apperr.Conflict("invoice cannot be cancelled").WithDetail("status", string(before))
			}
			return err
		}
		if err := s.Invoices.Update(ctx, inv); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: ActionInvoiceCancelled, ObjectType: "invoice", ObjectID: inv.ID.String(), Reason: ptr(reason),
			ActorType: audit.ActorAdmin, OrganizationID: ptr(inv.OrganizationID),
			Data: map[string]any{"number": inv.Number, "status_before": string(before)},
		})
	})
	if err != nil {
		return domain.Invoice{}, err
	}
	s.log.WarnContext(ctx, "invoice cancelled", logx.KeyOp, op, KeyInvoiceID, inv.ID, KeyAdminID, adminID)
	return inv, nil
}
