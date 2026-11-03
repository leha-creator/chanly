package app

import (
	"github.com/leha-creator/chanly/internal/payout/domain"
)

// ParseWebhook проверяет подпись и разбирает событие Рокет Ворк (R-11). Сервис в запросе
// не вызывается: событие несёт только идентификатор объекта, свежее состояние запросит
// обработчик inbound_event.
func (s *Service) ParseWebhook(header map[string][]string, body []byte) (domain.WebhookEvent, error) {
	if !s.Config.ProviderEnabled {
		return domain.WebhookEvent{}, domain.ErrProviderDisabled
	}
	return s.Provider.ParseWebhook(header, body)
}
