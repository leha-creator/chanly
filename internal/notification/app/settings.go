package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/notification/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
)

type SettingsResult struct {
	domain.Settings
	Catalog []domain.DeliveryOption `json:"catalog"`
}

func (s *Service) GetSettings(ctx context.Context, user uuid.UUID) (SettingsResult, error) {
	a, err := s.SettingsAudience.SettingsAccess(ctx, user)
	if err != nil {
		return SettingsResult{}, err
	}
	settings, err := s.SettingsRepo.Settings(ctx, user)
	return SettingsResult{Settings: settings, Catalog: settings.Catalog(a)}, err
}
func (s *Service) UpdateSettings(ctx context.Context, user uuid.UUID, patch domain.SettingsPatch) (SettingsResult, error) {
	var out SettingsResult
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		settings, err := s.SettingsRepo.LockSettings(ctx, user, s.Clock.Now())
		if err != nil {
			return err
		}
		a, err := s.SettingsAudience.SettingsAccess(ctx, user)
		if err != nil {
			return err
		}
		settings, err = settings.Apply(patch, a)
		if err != nil {
			return err
		}
		if err = s.SettingsRepo.SaveSettings(ctx, user, settings, s.Clock.Now()); err != nil {
			return err
		}
		out = SettingsResult{Settings: settings, Catalog: settings.Catalog(a)}
		return nil
	})
	if err == nil {
		s.log.InfoContext(ctx, "account settings updated", "op", "Notification.UpdateSettings", "version", out.Version)
	}
	return out, err
}

// deliveryPolicy is evaluated on every attempt, including after quiet hours or a retry.
func (s *Service) deliveryPolicy(ctx context.Context, n domain.Notification) (bool, error) {
	if s.SettingsRepo == nil {
		return true, nil
	}
	settings, err := s.SettingsRepo.Settings(ctx, n.UserID)
	if err != nil {
		return false, err
	}
	channel := string(n.Channel)
	group := n.SettingsGroup()
	if !settings.Enabled(group, n.Template, channel) {
		return false, nil
	}
	if !domain.Mandatory(n.Template, channel) {
		if delay := settings.QuietHours.QuietDelay(s.Clock.Now()); delay > 0 {
			return false, job.Postpone(errors.New("quiet_hours"), delay)
		}
	}
	return true, nil
}
