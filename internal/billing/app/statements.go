package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// ReasonUnmatched — оповещение о несопоставленном поступлении (R-13, 07 §3); объект — id поступления.
const ReasonUnmatched = "bank_transaction_unmatched"

// ImportResult — итог загрузки выписки (как в adm/payments).
type ImportResult struct {
	StatementID uuid.UUID
	Incoming    int
	Matched     int
	Unmatched   int
	Duplicates  int
	// Credited — сумма зачисленных на балансы поступлений.
	Credited money.Kopecks
}

// BankTransactionPage — страница поступлений; NextCursor пуст на последней странице.
type BankTransactionPage struct {
	Items      []domain.BankTransaction
	NextCursor string
}

// ImportStatement загружает выписку 1С (S-30, R-12): тот же файл дважды не принимается,
// выписка только по расчётному счёту платформы, файл — в хранилище, каждое поступление
// сопоставляется и зачисляется в своей транзакции; повтор операции из другой выписки
// пропускается (05 §5 шаг 1).
func (s *Service) ImportStatement(ctx context.Context, adminID uuid.UUID, data []byte) (ImportResult, error) {
	const op = "Billing.ImportStatement"
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	exists, err := s.Statements.ExistsByHash(ctx, sha)
	if err != nil {
		return ImportResult{}, err
	}
	if exists {
		s.log.WarnContext(ctx, "bank statement rejected", logx.KeyOp, op, "reason", "already_imported", KeyAdminID, adminID)
		return ImportResult{}, domain.ErrStatementImported
	}
	parsed, err := s.Parser.Parse(data)
	if err != nil {
		s.log.WarnContext(ctx, "bank statement rejected", logx.KeyOp, op, "reason", "format", KeyAdminID, adminID,
			logx.KeyError, err.Error())
		return ImportResult{}, err
	}
	if parsed.Account != s.Config.Supplier.BankAccount {
		s.log.WarnContext(ctx, "bank statement rejected", logx.KeyOp, op, "reason", "account", KeyAdminID, adminID,
			"account", logx.MaskAccount(parsed.Account))
		return ImportResult{}, domain.ErrStatementAccount
	}
	now := s.Clock.Now()
	st := domain.BankStatement{
		ID: ids.New(), UploadedBy: adminID, FileSHA256: sha, Account: parsed.Account, DateFrom: parsed.DateFrom,
		DateTo: parsed.DateTo, Opening: parsed.Opening, Closing: parsed.Closing, IncomingCount: len(parsed.Incoming),
		CreatedAt: now, UpdatedAt: now,
	}
	st.StorageKey = "statements/" + st.ID.String()
	if err := s.Storage.PutPrivate(ctx, st.StorageKey, bytes.NewReader(data), int64(len(data)), "text/plain"); err != nil {
		return ImportResult{}, err
	}
	if err := s.Tx.InTx(ctx, func(ctx context.Context) error { return s.Statements.Insert(ctx, st) }); err != nil {
		return ImportResult{}, err
	}
	res := ImportResult{StatementID: st.ID, Incoming: len(parsed.Incoming)}
	for _, in := range parsed.Incoming {
		outcome, err := s.importIncoming(ctx, op, st.ID, in)
		if err != nil {
			// Уже зачисленное остаётся; повторная загрузка того же файла запрещена, поэтому
			// остаток загружается выпиской за следующий период или вручную.
			s.log.ErrorContext(ctx, "bank statement import interrupted", logx.KeyOp, op, KeyBankStatementID, st.ID,
				"processed", res.Matched+res.Unmatched+res.Duplicates, logx.KeyError, err.Error())
			s.finishStatement(ctx, st, res)
			return res, err
		}
		switch {
		case outcome.duplicate:
			res.Duplicates++
		case outcome.matched:
			res.Matched++
			res.Credited += in.Amount
		default:
			res.Unmatched++
		}
	}
	if err := s.finishStatement(ctx, st, res); err != nil {
		return res, err
	}
	s.log.InfoContext(ctx, "bank statement imported", logx.KeyOp, op, KeyBankStatementID, st.ID, "incoming", res.Incoming,
		"matched", res.Matched, "unmatched", res.Unmatched, "duplicates", res.Duplicates, KeyAdminID, adminID)
	return res, nil
}

// finishStatement сохраняет счётчики выписки и запись журнала действий.
func (s *Service) finishStatement(ctx context.Context, st domain.BankStatement, res ImportResult) error {
	st.MatchedCount, st.UnmatchedCount, st.DuplicateCount = res.Matched, res.Unmatched, res.Duplicates
	st.UpdatedAt = s.Clock.Now()
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Statements.UpdateCounts(ctx, st); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: ActionStatementImported, ObjectType: "bank_statement", ObjectID: st.ID.String(), ActorType: audit.ActorAdmin,
			Data: map[string]any{"incoming": res.Incoming, "matched": res.Matched, "unmatched": res.Unmatched,
				"duplicates": res.Duplicates, "credited": int64(res.Credited)},
		})
	})
	if err != nil {
		s.log.ErrorContext(ctx, "bank statement counts not saved", logx.KeyOp, "Billing.ImportStatement",
			KeyBankStatementID, st.ID, logx.KeyError, err.Error())
	}
	return err
}

type incomingOutcome struct {
	duplicate bool
	matched   bool
}

// importIncoming — одно поступление в своей транзакции: кандидаты под блокировкой, запись
// поступления (повтор external_id — пропуск), при сопоставлении — проводка deposit и оплата счёта.
func (s *Service) importIncoming(ctx context.Context, op string, statementID uuid.UUID, in domain.Incoming) (incomingOutcome, error) {
	var (
		out incomingOutcome
		bt  domain.BankTransaction
		res domain.MatchResult
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var c domain.Candidates
		var err error
		if c.ByNumber, err = s.Invoices.ByNumbersForUpdate(ctx, domain.FindNumbers(in.Purpose)); err != nil {
			return err
		}
		if c.ByINNAndAmount, err = s.Invoices.IssuedByINNAndAmountForUpdate(ctx, in.PayerINN, in.Amount); err != nil {
			return err
		}
		res = domain.Match(in, c)
		now := s.Clock.Now()
		bt = domain.BankTransaction{ID: ids.New(), StatementID: statementID, Incoming: in, MatchStatus: domain.MatchUnmatched,
			CreatedAt: now, UpdatedAt: now}
		if res.Matched {
			bt.MatchStatus, bt.OrganizationID, bt.InvoiceID = domain.MatchMatched, res.OrganizationID, res.Invoice.ID
		}
		inserted, err := s.BankTransactions.InsertIfNew(ctx, bt)
		if err != nil {
			return err
		}
		if !inserted {
			out.duplicate = true
			return nil
		}
		if !res.Matched {
			return nil
		}
		out.matched = true
		return s.credit(ctx, bt, res.Invoice, uuid.Nil, now)
	})
	if err != nil || out.duplicate {
		if out.duplicate {
			s.log.InfoContext(ctx, "payment duplicate skipped", logx.KeyOp, op, KeyBankStatementID, statementID,
				KeyAmount, int64(in.Amount))
		}
		return out, err
	}
	if !out.matched {
		s.log.WarnContext(ctx, "payment unmatched", logx.KeyOp, op, KeyBankTransactionID, bt.ID, KeyAmount, int64(in.Amount),
			"payer_inn", logx.MaskINN(in.PayerINN))
		s.raise(ctx, op, alert.Alert{Reason: ReasonUnmatched, ObjectKey: bt.ID.String(), Level: alert.LevelWarn,
			Summary: "Поступление по выписке не сопоставлено со счётом",
			Fields:  map[string]any{KeyBankTransactionID: bt.ID.String(), KeyAmount: int64(in.Amount)}})
		return out, nil
	}
	s.credited(ctx, op, bt, res.Invoice, res.Method)
	return out, nil
}

// credit — проводка deposit, оплата счёта и уведомление в транзакции вызывающего: уведомление
// создаётся вместе с зачислением или не создаётся вовсе (I-10).
func (s *Service) credit(ctx context.Context, bt domain.BankTransaction, inv *domain.Invoice, adminID uuid.UUID, now time.Time) error {
	req := domain.DepositRequest{BankTransactionID: bt.ID, OrganizationID: bt.OrganizationID, Amount: bt.Amount, AdminID: adminID}
	if inv != nil {
		req.InvoiceID = inv.ID
	}
	if err := s.Ledger.Deposit(ctx, req); err != nil {
		return err
	}
	data := map[string]any{KeyAmount: int64(bt.Amount)}
	if inv != nil {
		if err := inv.MarkPaid(bt.Amount, now); err != nil {
			return err
		}
		if err := s.Invoices.Update(ctx, *inv); err != nil {
			return err
		}
		data["invoice_number"] = inv.Number
	}
	return s.Notifier.Notify(ctx, domain.Notice{Template: domain.NoticeBalanceDeposited, OrganizationID: bt.OrganizationID,
		ObjectType: "bank_transaction", ObjectID: bt.ID.String(), Data: data})
}

// credited — журнал после фиксации зачисления.
func (s *Service) credited(ctx context.Context, op string, bt domain.BankTransaction, inv *domain.Invoice, by domain.MatchMethod) {
	args := []any{logx.KeyOp, op, KeyBankTransactionID, bt.ID, logx.KeyOrganizationID, bt.OrganizationID,
		KeyAmount, int64(bt.Amount), "by", string(by)}
	if inv != nil {
		args = append(args, KeyInvoiceID, inv.ID, "invoice_amount", int64(inv.Amount))
	}
	s.log.InfoContext(ctx, "payment credited", args...)
}

// MatchManually — администратор относит несопоставленное или пропущенное поступление к
// организации и, если указан, к счёту (R-13): проводка deposit от администратора.
func (s *Service) MatchManually(ctx context.Context, bankTxID, orgID, invoiceID, adminID uuid.UUID, reason string) (domain.BankTransaction, error) {
	const op = "Billing.MatchManually"
	if reason == "" {
		return domain.BankTransaction{}, apperr.Validation("reason", "required")
	}
	var (
		bt  domain.BankTransaction
		inv *domain.Invoice
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if bt, err = s.BankTransactions.GetForUpdate(ctx, bankTxID); err != nil {
			return notFound(err, "bank transaction")
		}
		if bt.MatchStatus == domain.MatchMatched {
			return apperr.Conflict("bank transaction is already matched").WithDetail("status", string(bt.MatchStatus))
		}
		org, err := s.Organizations.Requisites(ctx, orgID)
		if err != nil {
			return notFound(err, "organization")
		}
		if org.IsPlatform {
			return apperr.Validation("organization", "platform")
		}
		if invoiceID != uuid.Nil {
			got, err := s.Invoices.GetForUpdate(ctx, invoiceID)
			if err != nil {
				return notFound(err, "invoice")
			}
			if got.OrganizationID != orgID {
				return apperr.Validation("invoice", "another_organization")
			}
			if !got.Payable() {
				return apperr.Conflict("invoice cannot be paid").WithDetail("status", string(got.Status))
			}
			inv = &got
		}
		now := s.Clock.Now()
		before := bt.MatchStatus
		bt.MatchStatus, bt.OrganizationID, bt.MatchedBy, bt.IgnoredReason, bt.UpdatedAt = domain.MatchMatched, orgID, adminID, "", now
		bt.InvoiceID = invoiceID
		if err := s.BankTransactions.Update(ctx, bt); err != nil {
			return err
		}
		if err := s.credit(ctx, bt, inv, adminID, now); err != nil {
			return err
		}
		data := map[string]any{KeyAmount: int64(bt.Amount), "status_before": string(before)}
		if inv != nil {
			data["invoice_number"] = inv.Number
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: ActionPaymentMatched, ObjectType: "bank_transaction", ObjectID: bt.ID.String(), Reason: ptr(reason),
			ActorType: audit.ActorAdmin, OrganizationID: ptr(orgID), Data: data,
		})
	})
	if err != nil {
		return domain.BankTransaction{}, err
	}
	s.log.WarnContext(ctx, "payment matched manually", logx.KeyOp, op, KeyBankTransactionID, bt.ID, logx.KeyOrganizationID, orgID,
		KeyAdminID, adminID, KeyAmount, int64(bt.Amount))
	s.resolveUnmatched(ctx, op, bt.ID)
	s.credited(ctx, op, bt, inv, domain.MatchManually)
	return bt, nil
}

// Ignore — поступление не относится к пополнениям (возврат, ошибочный платёж): ignored с
// основанием, без проводки (R-13).
func (s *Service) Ignore(ctx context.Context, bankTxID, adminID uuid.UUID, reason string) (domain.BankTransaction, error) {
	const op = "Billing.IgnorePayment"
	if reason == "" {
		return domain.BankTransaction{}, apperr.Validation("reason", "required")
	}
	var bt domain.BankTransaction
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if bt, err = s.BankTransactions.GetForUpdate(ctx, bankTxID); err != nil {
			return notFound(err, "bank transaction")
		}
		if bt.MatchStatus != domain.MatchUnmatched {
			return apperr.Conflict("bank transaction is not unmatched").WithDetail("status", string(bt.MatchStatus))
		}
		bt.MatchStatus, bt.IgnoredReason, bt.MatchedBy, bt.UpdatedAt = domain.MatchIgnored, reason, adminID, s.Clock.Now()
		if err := s.BankTransactions.Update(ctx, bt); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: ActionPaymentIgnored, ObjectType: "bank_transaction", ObjectID: bt.ID.String(), Reason: ptr(reason),
			ActorType: audit.ActorAdmin, Data: map[string]any{KeyAmount: int64(bt.Amount)},
		})
	})
	if err != nil {
		return domain.BankTransaction{}, err
	}
	s.log.WarnContext(ctx, "payment ignored", logx.KeyOp, op, KeyBankTransactionID, bt.ID, KeyAdminID, adminID,
		KeyAmount, int64(bt.Amount))
	s.resolveUnmatched(ctx, op, bt.ID)
	return bt, nil
}

// resolveUnmatched закрывает оповещение о поступлении; ошибка — только в журнал.
func (s *Service) resolveUnmatched(ctx context.Context, op string, id uuid.UUID) {
	if s.Alerts == nil {
		return
	}
	if err := s.Alerts.Resolve(ctx, ReasonUnmatched, id.String()); err != nil {
		s.log.ErrorContext(ctx, "alert resolve failed", logx.KeyOp, op, "reason", ReasonUnmatched, logx.KeyError, err.Error())
	}
}

// ListBankTransactions — поступления, новые сверху; status пуст — все.
func (s *Service) ListBankTransactions(ctx context.Context, status domain.MatchStatus, cursorStr string, limit int) (BankTransactionPage, error) {
	switch status {
	case "", domain.MatchMatched, domain.MatchUnmatched, domain.MatchIgnored:
	default:
		return BankTransactionPage{}, apperr.Validation("status", "invalid")
	}
	after, err := cursor(cursorStr)
	if err != nil {
		return BankTransactionPage{}, err
	}
	limit = pageLimit(limit)
	list, err := s.BankTransactions.List(ctx, status, after, limit+1)
	if err != nil {
		return BankTransactionPage{}, err
	}
	page := BankTransactionPage{}
	if len(list) > limit {
		list = list[:limit]
		last := list[limit-1]
		page.NextCursor = domain.Cursor{At: last.CreatedAt, ID: last.ID}.Encode()
	}
	page.Items = list
	return page, nil
}

// IsStatementRejection — ошибка загрузки выписки из-за самого файла (повтор, чужой счёт,
// формат, размер), а не сбой платформы.
func IsStatementRejection(err error) bool {
	for _, target := range []error{domain.ErrStatementImported, domain.ErrStatementAccount, domain.ErrStatementFormat,
		domain.ErrStatementTooLarge} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
