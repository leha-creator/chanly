package bootstrap

import (
	"context"
	"errors"
	"fmt"

	channelapp "github.com/leha-creator/chanly/internal/channel/app"
	"github.com/leha-creator/chanly/internal/integration/max"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// registerMAXBotStarted routes authenticated, token-redacted MAX updates to
// their owning module while preserving existing channel connection links.
func (m *modules) registerMAXBotStarted() {
	m.platform.dispatcher.Handle(inbound.SourceMAX, max.UpdateBotStarted, func(ctx context.Context, event inbound.Event) error {
		update, err := max.ParseUpdate(event.Payload)
		if err != nil {
			return job.Permanent(fmt.Errorf("parse MAX bot start: %w", err))
		}
		if update.User == nil || update.User.IsBot || update.UserID() <= 0 {
			return inbound.ErrIgnore
		}
		if update.NotificationTokenHash != "" {
			return m.account.ConfirmMAXBotConnection(ctx, update.UserID(), update.NotificationTokenHash)
		}
		if update.ConnectionTokenHash != "" {
			err = m.channel.StartConnectionHash(ctx, update.UserID(), update.ConnectionTokenHash)
		} else {
			err = m.channel.StartConnection(ctx, update.UserID(), update.Payload)
		}
		if errors.Is(err, channelapp.ErrIgnored) {
			return inbound.ErrIgnore
		}
		return err
	})
}
