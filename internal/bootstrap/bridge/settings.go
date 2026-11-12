package bridge

import (
	"context"

	"github.com/google/uuid"

	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/notification/domain"
)

type NotificationSettingsAudience struct{ Users *accountpg.Users }

func (b NotificationSettingsAudience) SettingsAccess(ctx context.Context, user uuid.UUID) (domain.SettingsAccess, error) {
	a, err := b.Users.SettingsAccess(ctx, user)
	if err != nil {
		return domain.SettingsAccess{}, err
	}
	out := domain.SettingsAccess{MaxBot: a.MaxBot}
	if a.Advertiser {
		out.Roles = append(out.Roles, domain.RoleAdvertiser)
	}
	if a.Blogger {
		out.Roles = append(out.Roles, domain.RoleBlogger)
	}
	if a.AdminRole != nil {
		out.Roles = append(out.Roles, domain.RoleAdmin)
	}
	return out, nil
}
