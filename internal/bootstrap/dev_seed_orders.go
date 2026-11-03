package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	placementapp "github.com/leha-creator/chanly/internal/placement/app"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
)

// seedCampaignWindow — окно кампаний заказов: сегодня … +2 дня по Москве.
func seedCampaignWindow(now time.Time) (from, to postdomain.Date) {
	from = postdomain.TodayMSK(now)
	return from, from.AddDate(0, 0, 2)
}

// seedPublishAt — время mode=at: ближайшее кратное step не раньше now + lead + 5 мин. В
// последние минуты суток по Москве (окно кампании — до 23:59) — 00:05 следующего дня.
func seedPublishAt(now time.Time, lead, step time.Duration) time.Time {
	at := now.Add(lead + 5*time.Minute)
	if t := at.Truncate(step); !t.Equal(at) {
		at = t.Add(step)
	}
	if m := at.In(msk); m.Hour() == 23 && m.Minute() >= 55 {
		next := time.Date(m.Year(), m.Month(), m.Day()+1, 0, 5, 0, 0, msk)
		return next.UTC()
	}
	return at.UTC()
}

// seedRocketWorkDeposit — пополнение депозита Рокет Ворк для сценария paid.
const seedRocketWorkDeposit money.Kopecks = 50_000_00

// seedScenario — заказ dev seed: до какого состояния довести размещение.
type seedScenario struct {
	name string
	// channel — индекс активного канала в s.channels.
	channel int
	// target — целевое состояние размещения.
	target placementdomain.Status
	// contractor — нужен активный исполнитель Рокет Ворк (самозанятый принимает заявку).
	contractor bool
	// claim — из published претензия рекламодателя; cancel — отмена заказа; reject — отказ модерации.
	claim, cancel, reject bool
	// pay — выплата доведена до paid через подделку Рокет Ворк.
	pay bool
}

// seedScenarios — по заказу на сценарий. Публикуемые сценарии стоят на разных каналах:
// топы размещений одного канала не пересекаются (I-06). Каналы 6 и 7 — блогера-юрлица.
var seedScenarios = []seedScenario{
	{name: "moderation", channel: 0, target: placementdomain.StatusCreated},
	{name: "rejected", channel: 1, target: placementdomain.StatusCreated, reject: true},
	{name: "cancelled", channel: 2, target: placementdomain.StatusCancelled, cancel: true},
	{name: "awaiting_blogger", channel: 3, target: placementdomain.StatusAwaitingBlogger},
	{name: "awaiting_schedule", channel: 4, target: placementdomain.StatusAwaitingSchedule, contractor: true},
	{name: "scheduled", channel: 5, target: placementdomain.StatusScheduled, contractor: true},
	{name: "published", channel: 0, target: placementdomain.StatusPublished, contractor: true},
	{name: "disputed", channel: 1, target: placementdomain.StatusDisputed, contractor: true, claim: true},
	{name: "removed", channel: 2, target: placementdomain.StatusRemoved, contractor: true},
	{name: "accepted", channel: 3, target: placementdomain.StatusAccepted, contractor: true},
	{name: "paid", channel: 4, target: placementdomain.StatusPaid, contractor: true, pay: true},
	{name: "bank_transfer", channel: 6, target: placementdomain.StatusAccepted},
}

// seedRun — заказ сценария в работе.
type seedRun struct {
	sc     seedScenario
	ch     seedChannel
	order  placementdomain.Order
	pid    uuid.UUID
	marked bool
}

func (r *seedRun) key(step string) string { return "dev-seed-" + r.sc.name + "-" + step }

// seedOrders проводит заказы по сценариям; сценарий с уже созданной кампанией пропускается.
// Сначала все заказы оформляются и одобряются, и erid для них worker получает разом; затем
// каждый доводится до своего состояния.
func (s *seeder) seedOrders(ctx context.Context) error {
	existing, err := s.campaignNames(ctx)
	if err != nil {
		return err
	}
	var runs []*seedRun
	for _, sc := range seedScenarios {
		name := "Демо: " + sc.name
		switch {
		case existing[name]:
			s.skip(ctx, sc.name, skipExists)
			continue
		case sc.contractor && s.rw == nil:
			s.skip(ctx, sc.name, skipRocketWorkNoFake)
			continue
		}
		r, err := s.startOrder(ctx, sc, name)
		if err != nil {
			return fmt.Errorf("scenario %s: %w", sc.name, err)
		}
		if r.marked {
			runs = append(runs, r)
		}
	}
	if err := s.wait.Wait(ctx, "mark the post versions and send them to bloggers", func(ctx context.Context) (bool, error) {
		for _, r := range runs {
			ok, err := s.placementIs(ctx, r.order.ID, placementdomain.StatusAwaitingBlogger)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}); err != nil {
		return err
	}
	for _, r := range runs {
		if err := s.finishOrder(ctx, r); err != nil {
			return fmt.Errorf("scenario %s: %w", r.sc.name, err)
		}
	}
	return nil
}

func (s *seeder) orderDone(ctx context.Context, r *seedRun) error {
	s.log.InfoContext(ctx, "seed order", logx.KeyOp, "Bootstrap.DevSeed", "order_id", r.order.ID, "order_number", r.order.Number,
		"scenario", r.sc.name, "placement_id", r.pid, "status", r.sc.target)
	s.note("заказ %d «%s» — %s (канал «%s»)", r.order.Number, r.sc.name, r.sc.target, r.ch.title)
	return nil
}

// startOrder оформляет заказ сценария и проводит модерацию версии. marked — версия одобрена
// и ждёт erid; остальные сценарии на этом закончены.
func (s *seeder) startOrder(ctx context.Context, sc seedScenario, name string) (*seedRun, error) {
	r := &seedRun{sc: sc, ch: s.channels[sc.channel]}
	campaign, err := s.campaign(ctx, name, "Демо-заказ «"+sc.name+"»: скидки до 30% — https://shop.example.test/sale")
	if err != nil {
		return nil, err
	}
	if _, err := s.m.placement.AddCartItem(ctx, s.adv.orgID, s.adv.userID, r.ch.id, "f_1_24"); err != nil {
		return nil, err
	}
	if r.order, err = s.m.placement.Checkout(ctx, s.adv.orgID, s.adv.userID, campaign.ID, r.key("checkout")); err != nil {
		return nil, err
	}
	if r.pid, err = s.placementID(ctx, r.order.ID); err != nil {
		return nil, err
	}
	versionID := r.order.CurrentPostVersionID
	switch {
	case sc.reject:
		if _, err := s.m.post.RejectVersion(ctx, versionID, s.moderator, "Уточните условия акции и срок её действия"); err != nil {
			return nil, err
		}
		return r, s.orderDone(ctx, r)
	case sc.cancel:
		if _, err := s.m.placement.CancelOrder(ctx, s.adv.orgID, s.adv.userID, r.order.ID, r.key("cancel")); err != nil {
			return nil, err
		}
		return r, s.orderDone(ctx, r)
	case sc.target == placementdomain.StatusCreated:
		return r, s.orderDone(ctx, r)
	}
	// Модерация и маркировка: при включённом ОРД erid выдаёт worker.
	if _, err := s.m.post.ApproveVersion(ctx, versionID, s.moderator); err != nil {
		return nil, err
	}
	if !s.cfg.ORD.Enabled {
		if _, err := s.m.post.MarkVersionReady(ctx, versionID, fmt.Sprintf("dev-%d", r.order.Number)); err != nil {
			return nil, err
		}
	}
	r.marked = true
	return r, nil
}

// finishOrder доводит размещение из awaiting_blogger до состояния сценария.
func (s *seeder) finishOrder(ctx context.Context, r *seedRun) error {
	sc, blg, pid, key := r.sc, r.ch.owner, r.pid, r.key
	done := func() error { return s.orderDone(ctx, r) }
	if sc.target == placementdomain.StatusAwaitingBlogger {
		return done()
	}
	if _, err := s.m.placement.Accept(ctx, blg.orgID, blg.userID, pid, key("accept")); err != nil {
		return err
	}
	if sc.target == placementdomain.StatusAwaitingSchedule {
		return done()
	}
	if sc.target == placementdomain.StatusScheduled {
		at := seedPublishAt(s.m.in.clock.Now(), s.cfg.Placement.MinLead, s.cfg.Placement.SlotStep)
		if _, err := s.m.placement.Schedule(ctx, blg.orgID, blg.userID, pid, placementdomain.ModeAt, &at, key("schedule")); err != nil {
			return err
		}
		return done()
	}
	if _, err := s.m.placement.Schedule(ctx, blg.orgID, blg.userID, pid, placementdomain.ModeNow, nil, key("publish")); err != nil {
		return err
	}
	if err := s.untilStatus(ctx, r.order.ID, "publish the post", placementdomain.StatusPublished); err != nil {
		return err
	}
	switch {
	case sc.target == placementdomain.StatusPublished:
		return done()
	case sc.claim:
		if _, err := s.m.placement.CreateClaim(ctx, s.adv.orgID, s.adv.userID, pid, placementapp.ClaimInput{
			Reason: placementdomain.ClaimRemovedEarly, Text: "Пост удалён раньше срока размещения", IdemKey: key("claim"),
		}); err != nil {
			return err
		}
		return done()
	}
	if _, err := s.m.placement.MarkRemovedByAdmin(ctx, pid, s.admin, s.m.in.clock.Now(), "Снято для демонстрации"); err != nil {
		return err
	}
	if sc.target == placementdomain.StatusRemoved {
		return done()
	}
	if _, err := s.m.placement.AcceptPlacement(ctx, s.adv.orgID, s.adv.userID, pid, key("accept-placement")); err != nil {
		return err
	}
	if !sc.pay {
		return done()
	}
	if err := s.payout(ctx, pid, r.order.ID); err != nil {
		return err
	}
	return done()
}

// payout доводит выплату размещения до paid: депозит, отправка worker, исполнение в подделке.
func (s *seeder) payout(ctx context.Context, pid, orderID uuid.UUID) error {
	res, err := s.m.ledger.DepositTopup(ctx, "dev-seed-rocketwork-deposit", seedRocketWorkDeposit, s.admin, s.moderator,
		"Депозит Рокет Ворк для тестовых данных")
	if err != nil {
		return err
	}
	if !res.Replayed {
		if err := s.rw.SetDeposit(s.rw.Deposit() + int64(seedRocketWorkDeposit)); err != nil {
			return err
		}
	}
	var p payoutdomain.Payout
	if err := s.wait.Wait(ctx, "send the payout to Rocket Work", func(ctx context.Context) (bool, error) {
		var err error
		p, err = s.m.payoutStore.LatestPayout(ctx, pid, false)
		if err != nil {
			return false, nil // выплату ещё не создало задание
		}
		return p.Status == payoutdomain.StatusSent && p.ExternalID != "", nil
	}); err != nil {
		return err
	}
	if err := s.rw.CompletePayout(p.ExternalID, -1); err != nil {
		return err
	}
	if err := s.m.payout.ApplyPayout(ctx, p.ExternalID); err != nil {
		return err
	}
	return s.untilStatus(ctx, orderID, "mark the placement paid", placementdomain.StatusPaid)
}

// placementID — единственное размещение заказа.
func (s *seeder) placementID(ctx context.Context, orderID uuid.UUID) (uuid.UUID, error) {
	v, err := s.m.placement.GetOrder(ctx, s.adv.orgID, orderID)
	if err != nil {
		return uuid.Nil, err
	}
	if len(v.Placements) != 1 {
		return uuid.Nil, fmt.Errorf("order %d has %d placements, want 1: clear the cart of %s", v.Order.Number,
			len(v.Placements), seedAdvEmail)
	}
	return v.Placements[0].Placement.ID, nil
}

// placementIs — размещение заказа в состоянии want.
func (s *seeder) placementIs(ctx context.Context, orderID uuid.UUID, want placementdomain.Status) (bool, error) {
	v, err := s.m.placement.GetOrder(ctx, s.adv.orgID, orderID)
	if err != nil {
		return false, err
	}
	return len(v.Placements) == 1 && v.Placements[0].Placement.Status == want, nil
}

// untilStatus ждёт, пока размещение заказа придёт в состояние want.
func (s *seeder) untilStatus(ctx context.Context, orderID uuid.UUID, what string, want placementdomain.Status) error {
	return s.wait.Wait(ctx, what, func(ctx context.Context) (bool, error) {
		return s.placementIs(ctx, orderID, want)
	})
}
