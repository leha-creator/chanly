package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/alert"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Send — задание send_payout (05 §4.3, R-6, R-7): проверки и CreatePayout вне транзакции,
// затем отметка sent и опрос в транзакции. attempt — номер выполнения задания.
func (s *Service) Send(ctx context.Context, payoutID uuid.UUID, attempt int) error {
	const op = "Payout.Send"
	p, err := s.Store.PayoutByID(ctx, payoutID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(err)
	}
	if err != nil {
		return err
	}
	if p.Status != domain.StatusCreated || p.Method != domain.MethodRocketwork {
		s.log.DebugContext(ctx, "payout send skipped", logx.KeyOp, op, KeyPayoutID, p.ID, "status", p.Status, "method", p.Method)
		return nil
	}
	log := s.log.With(logx.KeyOp, op, KeyPayoutID, p.ID, KeyPlacementID, p.PlacementID, KeyAttempt, p.Attempt,
		"job_attempt", attempt, KeyIdempotencyKey, p.IdempotencyKey)

	// R-6: прошлый запуск мог создать выплату с неизвестным исходом (таймаут, сбой разбора).
	// Сначала ищем её по нашему ключу: найдена — проверки перед отправкой уже не нужны, иначе
	// отказ проверки пометил бы failed выплату, деньги по которой ушли, и повтор заплатил бы дважды.
	found, ok, err := s.Provider.FindPayout(ctx, p.IdempotencyKey)
	if err != nil {
		return s.providerWait(ctx, op, err)
	}
	if ok {
		log.InfoContext(ctx, "payout found before send", KeyExternalID, found.ExternalID)
		return s.sent(ctx, log, p, found)
	}

	// R-7: исполнитель active по свежему статусу.
	c, err := s.Store.ContractorByID(ctx, p.ContractorID, false)
	if err != nil {
		return err
	}
	if c.Registered() && (c.Stale(s.Clock.Now(), s.Config.StatusMaxAge) || !c.CanAccept()) {
		pc, err := s.Provider.GetContractor(ctx, c.ExternalID)
		if err != nil {
			return s.providerWait(ctx, op, err)
		}
		if c, err = s.applyContractor(ctx, c.ID, pc); err != nil {
			return err
		}
	}
	if !c.CanAccept() {
		code := domain.FailureContractorNotActive
		if c.BlockedReason == string(domain.FailureTaxStatusLost) {
			code = domain.FailureTaxStatusLost
		}
		return s.fail(ctx, p.ID, code, "contractor is "+string(c.Status))
	}

	// Депозит не меньше суммы; иначе ждём час, попытка не тратится (05 §4.3).
	deposit, err := s.Provider.GetDepositBalance(ctx)
	if err != nil {
		return s.providerWait(ctx, op, err)
	}
	s.Metrics.Deposit("api", deposit)
	if deposit < p.Amount {
		return s.depositShort(ctx, p, int64(deposit))
	}

	brief, err := s.Placements.Brief(ctx, []uuid.UUID{p.PlacementID})
	if err != nil {
		return err
	}
	order := domain.PayoutOrder{IdempotencyKey: p.IdempotencyKey, PayoutID: p.ID, ContractorExternal: c.ExternalID,
		Amount: p.Amount, Purpose: purpose(brief[p.PlacementID])}
	pp, err := s.Provider.CreatePayout(ctx, order)
	if err != nil {
		if rej, ok := domain.AsRejected(err); ok {
			if rej.Code == domain.FailureInsufficientDeposit {
				return s.depositShort(ctx, p, int64(deposit))
			}
			return s.fail(ctx, p.ID, rej.Code, rej.Message)
		}
		if !errors.Is(err, domain.ErrProviderUnavailable) {
			return s.providerWait(ctx, op, err)
		}
		// Неизвестный исход (R-6): ищем выплату по нашему ключу, иначе повтор с тем же ключом.
		found, ok, ferr := s.Provider.FindPayout(ctx, p.IdempotencyKey)
		if ferr != nil || !ok {
			log.WarnContext(ctx, "payout send retry", logx.KeyError, err.Error(), "found", ok)
			return err
		}
		log.InfoContext(ctx, "payout found after unknown outcome", KeyExternalID, found.ExternalID)
		pp = found
	}
	return s.sent(ctx, log, p, pp)
}

// sent — выплата создана в сервисе: отметка sent и опрос, затем перенос ответа. После отметки
// исход принадлежит poll_payout: ошибка переноса не возвращается заданию, иначе исчерпание
// send_payout пометило бы failed выплату, деньги по которой уже ушли.
func (s *Service) sent(ctx context.Context, log *slog.Logger, p domain.Payout, pp domain.ProviderPayout) error {
	if err := s.markSent(ctx, p.ID, pp.ExternalID); err != nil {
		return err
	}
	log.InfoContext(ctx, "payout sent", KeyExternalID, pp.ExternalID, KeyAmount, int64(p.Amount))
	s.notify(ctx, domain.Notice{Template: domain.NoticePayoutSent, OrganizationID: p.BloggerOrganizationID, ObjectID: p.ID, PlacementID: p.PlacementID,
		Data: map[string]any{"amount": int64(p.Amount)}})
	s.resolve(ctx, AlertDepositInsufficient, payoutObject(p.ID))
	if _, err := s.apply(ctx, p.ID, pp); err != nil {
		log.WarnContext(ctx, "payout outcome left to poll", KeyExternalID, pp.ExternalID, logx.KeyError, err.Error())
	}
	return nil
}

func purpose(b domain.PlacementBrief) string {
	if b.OrderNumber == 0 {
		return "Вознаграждение за размещение рекламы"
	}
	return fmt.Sprintf("Вознаграждение за размещение рекламы, заказ №%d, канал «%s»", b.OrderNumber, b.ChannelTitle)
}

// depositShort — депозита не хватает: выплата остаётся created, ERROR и оповещение, повтор через час.
func (s *Service) depositShort(ctx context.Context, p domain.Payout, deposit int64) error {
	s.log.ErrorContext(ctx, "rocketwork deposit insufficient", logx.KeyOp, "Payout.Send", KeyPayoutID, p.ID,
		"deposit", deposit, "required", int64(p.Amount), "retry_in", s.Config.DepositRetry.String())
	s.raise(ctx, alert.LevelError, AlertDepositInsufficient, payoutObject(p.ID), "rocketwork deposit is not enough for a payout",
		map[string]any{"payout_id": p.ID.String(), "deposit": deposit, "required": int64(p.Amount)})
	return job.Postpone(fmt.Errorf("%w: deposit %d < %d", errDepositShort, deposit, p.Amount), s.Config.DepositRetry)
}

var errDepositShort = errors.New("rocketwork deposit is insufficient")

// markSent — отметка sent и опрос (R-10) в одной транзакции.
func (s *Service) markSent(ctx context.Context, id uuid.UUID, externalID string) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.Store.PayoutByID(ctx, id, true)
		if err != nil {
			return err
		}
		changed, err := p.MarkSent(externalID, s.Clock.Now())
		if err != nil || !changed {
			return err
		}
		if err := s.Store.UpdatePayout(ctx, &p); err != nil {
			return err
		}
		return s.Jobs.Enqueue(ctx, job.Job{Kind: KindPollPayout, DedupeKey: PollPayoutKey(p.ID),
			Payload: PayoutPayload{PayoutID: p.ID}, RunAt: s.Clock.Now().Add(s.Config.PollFast), MaxAttempts: PollAttempts})
	})
}

// SendExhausted — попытки send_payout исчерпаны (R-8). Только выплата, оставшаяся created:
// сначала поиск по ключу — найдена в сервисе — sent и опрос; не найдена — failed provider_error
// и оповещение; исход неизвестен — failed не ставится (деньги могли уйти), оповещение о расхождении.
func (s *Service) SendExhausted(ctx context.Context, payoutID uuid.UUID, cause error) {
	const op = "Payout.SendExhausted"
	log := s.log.With(logx.KeyOp, op, KeyPayoutID, payoutID)
	log.ErrorContext(ctx, "payout send exhausted", logx.KeyError, cause.Error())
	p, err := s.Store.PayoutByID(ctx, payoutID, false)
	if err != nil {
		log.ErrorContext(ctx, "payout fail after exhaustion failed", logx.KeyError, err.Error())
		return
	}
	if p.Status != domain.StatusCreated || p.Method != domain.MethodRocketwork {
		return
	}
	found, ok, ferr := s.Provider.FindPayout(ctx, p.IdempotencyKey)
	switch {
	case ferr != nil:
		log.ErrorContext(ctx, "payout outcome unknown after exhaustion", KeyIdempotencyKey, p.IdempotencyKey, logx.KeyError, ferr.Error())
		s.raise(ctx, alert.LevelError, AlertStateMismatch, payoutObject(p.ID), "payout outcome is unknown: check it in rocketwork",
			map[string]any{"payout_id": p.ID.String(), "placement_id": p.PlacementID.String()})
		return
	case ok:
		if err := s.sent(ctx, log, p, found); err != nil {
			log.ErrorContext(ctx, "payout fail after exhaustion failed", logx.KeyError, err.Error())
		}
		return
	}
	if err := s.fail(ctx, payoutID, domain.FailureProviderError, cause.Error()); err != nil && !errors.Is(err, domain.ErrInvalidPayoutState) {
		log.ErrorContext(ctx, "payout fail after exhaustion failed", logx.KeyError, err.Error())
	}
}
