package app

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Formats — активные форматы размещения по порядку (03 §2).
func (s *Service) Formats(ctx context.Context) ([]domain.Format, error) {
	return s.Deps.Formats.ListActive(ctx)
}

// FeeRateBP — комиссия платформы для цен рекламодателя (06 §3).
func (s *Service) FeeRateBP() int { return s.Config.FeeRateBP }

// TotalAmount — цена для рекламодателя по цене блогера.
func (s *Service) TotalAmount(amount money.Kopecks) money.Kopecks {
	return domain.TotalAmount(amount, s.Config.FeeRateBP)
}

// SetPrices заменяет набор цен канала организации (S-13, R-4, R-5) в одной транзакции
// с журналом действий.
func (s *Service) SetPrices(ctx context.Context, orgID, channelID uuid.UUID, prices []domain.Price) (*domain.Channel, error) {
	const op = "Channel.SetPrices"
	var out *domain.Channel
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Channels.GetForOrganizationForUpdate(ctx, orgID, channelID)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("channel not found")
		}
		if err != nil {
			return err
		}
		formats, err := s.Deps.Formats.ListActive(ctx)
		if err != nil {
			return err
		}
		before := priceMap(c.Prices)
		if err := c.SetPrices(prices, formats, s.Clock.Now()); err != nil {
			reason := "not_editable"
			var re *domain.RuleError
			if errors.As(err, &re) {
				reason = re.Reason
			}
			s.log.WarnContext(ctx, "channel prices rejected", logx.KeyOp, op, KeyChannelID, c.ID, "reason", reason, "status", c.Status)
			if errors.Is(err, domain.ErrNotEditable) {
				return notEditable(c)
			}
			return ruleError(err)
		}
		if err := s.Prices.Replace(ctx, c.ID, c.Prices, s.Clock.Now()); err != nil {
			return err
		}
		if err := s.save(ctx, op, c, domain.StatusChange{ChannelID: c.ID, From: c.Status, To: c.Status}); err != nil {
			return err
		}
		after := priceMap(c.Prices)
		changed := []string{}
		for _, k := range unionKeys(before, after) {
			if before[k] != after[k] {
				changed = append(changed, k)
			}
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "channel.prices_updated", ObjectType: "channel", ObjectID: c.ID.String(),
			Data: audit.Diff(before, after, changed), ActorType: audit.ActorBlogger,
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "channel prices updated", logx.KeyOp, op, KeyChannelID, c.ID, "enabled_formats", c.EnabledFormats())
		out = c
		return nil
	})
	return out, err
}

// priceMap — цены для журнала действий: формат → «сумма, включена».
func priceMap(prices []domain.Price) map[string]any {
	m := make(map[string]any, len(prices))
	for _, p := range prices {
		state := "off"
		if p.IsEnabled {
			state = "on"
		}
		m[p.FormatID] = strconv.FormatInt(int64(p.Amount), 10) + " " + state
	}
	return m
}

func unionKeys(a, b map[string]any) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}
