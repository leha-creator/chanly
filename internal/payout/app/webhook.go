package app

import (
	"crypto/sha256"
	"fmt"

	"github.com/leha-creator/chanly/internal/payout/domain"
)

// ParseWebhook проверяет подпись и разбирает событие Рокет Ворк (R-11). Сервис в запросе
// не вызывается: событие несёт только идентификатор объекта, свежее состояние запросит
// обработчик inbound_event.
func (s *Service) ParseWebhook(header map[string][]string, body []byte) (domain.WebhookEvent, error) {
	if !s.Config.ProviderEnabled {
		return domain.WebhookEvent{}, domain.ErrProviderDisabled
	}
	e, err := s.Provider.ParseWebhook(header, body)
	e.ScopeKey = s.WebhookScopeKey()
	return e, err
}

func (s *Service) WebhookScopeKey() string {
	if s.Config.ProviderScope == nil {
		return ""
	}
	scope := s.Config.ProviderScope
	return fmt.Sprintf("%x", sha256.Sum256([]byte(scope.Environment+"\x00"+scope.Account)))
}
