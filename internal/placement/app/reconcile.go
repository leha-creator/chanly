package app

import (
	"context"
	"errors"
	"hash/fnv"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// reconcilePage — сколько живых публикаций читать за раз.
const reconcilePage = 200

// spread — смещение проверки публикации внутри интервала сверки: проверки распределены по
// времени равномерно, чтобы уложиться в лимиты MAX (05 §2.8).
func spread(id uuid.UUID, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write(id[:])
	return time.Duration(h.Sum64() % uint64(interval))
}

// ReconcilePublications — периодическое задание reconcile_publications (T-09, R-12): ставит
// check_publication на каждую живую публикацию со смещением внутри интервала и проверяет,
// что у просроченных scheduled есть задание публикации (05 §2.8 шаг 5).
func (s *Service) ReconcilePublications(ctx context.Context) error {
	const op = "Placement.ReconcilePublications"
	now := s.Clock.Now()
	r := s.Config.Control
	scheduledChecks, total := 0, 0
	after := uuid.Nil
	for {
		page, err := s.Publications.ListLive(ctx, after, reconcilePage)
		if err != nil {
			return err
		}
		for _, pub := range page {
			if err := s.Jobs.Enqueue(ctx, toJob(domain.CheckPublicationJob(pub.ID, now.Add(spread(pub.ID, r.ReconcileInterval))))); err != nil {
				return err
			}
			total++
		}
		if len(page) < reconcilePage {
			break
		}
		after = page[len(page)-1].ID
	}
	refs, err := s.Placements.ScheduledWithoutPublication(ctx, now.Add(-r.ScheduledGrace))
	if err != nil {
		return err
	}
	recreated := 0
	for _, ref := range refs {
		scheduledChecks++
		ok, err := s.Jobs.Exists(ctx, domain.PublishKey(ref.ID))
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if err := s.Jobs.Enqueue(ctx, toJob(domain.PublishJob(ref.ID, now))); err != nil {
			return err
		}
		recreated++
		s.log.ErrorContext(ctx, "publish job missing, recreated", logx.KeyOp, op, KeyPlacementID, ref.ID, "publish_at", ref.PublishAt)
	}
	s.log.InfoContext(ctx, "publications reconciliation scheduled", logx.KeyOp, op, "publications", total,
		"scheduled_checked", scheduledChecks, "recreated", recreated)
	return nil
}

// CheckPublication — задание check_publication (05 §2.8, R-12): сообщение на месте, содержание
// не изменено, «часы в топе» не нарушены; просмотры не чаще раза в час. Вызовы MAX — вне
// транзакции с приоритетом сверки.
func (s *Service) CheckPublication(ctx context.Context, publicationID uuid.UUID, j JobRef) error {
	const op = "Placement.CheckPublication"
	ctx = logx.With(ctx, "publication_id", publicationID.String())
	ctx = domain.WithCallPriority(ctx, domain.CallReconcile)
	var pub domain.Publication
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		pub, err = s.Publications.GetForUpdate(ctx, publicationID)
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !pub.Live() {
		s.log.DebugContext(ctx, "publication check skipped", logx.KeyOp, op, "reason", "removed")
		return nil
	}
	p, err := s.Placements.Get(ctx, pub.PlacementID)
	if err != nil {
		return err
	}
	ctx = logx.With(ctx, KeyPlacementID, p.ID.String())
	lp := domain.LivePlacement{Placement: p, Publication: pub}
	offers, err := s.Channels.Offers(ctx, []uuid.UUID{p.ChannelID})
	if err != nil {
		return err
	}
	chatID := offers[p.ChannelID].MaxChatID
	now := s.Clock.Now()
	bot, err := s.Publisher.BotUserID(ctx)
	if err != nil {
		return err
	}
	var msg *domain.ChannelMessage
	m, err := s.Publisher.GetMessage(ctx, pub.MaxMessageID)
	switch {
	case errors.Is(err, domain.ErrMessageNotFound):
	case err != nil:
		return err
	default:
		msg = &m
	}
	var recent []domain.ChannelMessage
	from, to, topChecked := domain.TopCheckWindow(&p, now, s.Config.Control)
	if msg != nil && topChecked {
		if recent, err = s.Publisher.ListMessages(ctx, chatID, from, to); err != nil {
			return err
		}
	}
	res := domain.Reconcile(lp, msg, recent, topChecked, bot, now, s.Config.Control)
	s.log.DebugContext(ctx, "publication checked", logx.KeyOp, op, "found", msg != nil,
		"hash_matches", msg != nil && msg.ContentHash == pub.ContentHash, "top_checked", topChecked && msg != nil,
		"violations", len(res.Violations), "views_updated", res.UpdateViews)
	for _, v := range res.Violations {
		if err := s.reportViolation(ctx, op, v, source{JobID: &j.ID}); err != nil {
			return err
		}
	}
	if !res.UpdateViews {
		return nil
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		pub, err := s.Publications.GetForUpdate(ctx, publicationID)
		if err != nil {
			return err
		}
		pub.UpdateViews(res.Views, s.Clock.Now())
		return s.Publications.Update(ctx, pub)
	})
}
