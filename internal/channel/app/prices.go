package app

import (
	"context"
	"slices"
	"strconv"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/channel/domain"
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
	return s.saveBlogger(ctx, orgID, channelID, nil, &prices)
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
