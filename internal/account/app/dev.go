package app

import (
	"context"
	"errors"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// DevEnsureUser возвращает пользователя с почтой email, создавая его без входа по коду.
// Только для maxim dev seed (APP_ENV development/test): тестовые данные заводятся через
// сервисы, а не SQL. Повтор возвращает того же пользователя с created = false.
func (s *Service) DevEnsureUser(ctx context.Context, email string) (domain.User, bool, error) {
	const op = "Account.DevEnsureUser"
	email = domain.NormalizeEmail(email)
	if email == "" {
		return domain.User{}, false, apperr.Validation("email", "required")
	}
	var (
		user    domain.User
		created bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		u, err := s.Users.GetByEmail(ctx, email)
		if errors.Is(err, domain.ErrNotFound) {
			u = domain.NewUser(ids.New(), email, s.Clock.Now())
			if err := s.Users.Create(ctx, u); err != nil {
				return err
			}
			created = true
		} else if err != nil {
			return err
		}
		user = *u
		return nil
	})
	if err != nil {
		return domain.User{}, false, err
	}
	s.log.InfoContext(ctx, "dev user ensured", logx.KeyOp, op, logx.KeyUserID, user.ID,
		"email", logx.MaskEmail(email), "created", created)
	return user, created, nil
}
