package app

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Запросы субъекта ПДн (152-ФЗ ст. 14, 21; этап «API администратора», R-26): выгрузка и
// обезличивание по почте. Исполняет администратор командой maxim admin pd …; почта и
// содержимое выгрузки в журнал приложения не пишутся — только идентификаторы.

// SubjectData — данные субъекта для ответа на запрос доступа.
type SubjectData struct {
	User         domain.User
	Organization *domain.Organization
	Members      int
	Acceptances  []domain.SubjectAcceptance
	Sessions     []domain.SubjectSession
}

// AnonymizeResult — что сделано при обезличивании.
type AnonymizeResult struct {
	UserID uuid.UUID
	// Changed — false: пользователь уже обезличен, ничего не менялось.
	Changed bool
	// OrganizationCleared — стёрты контакты организации (субъект — единственный участник).
	OrganizationCleared bool
	OrganizationID      *uuid.UUID
	SessionsCleared     int64
	CodesDeleted        int64
}

func (s *Service) personalData() (domain.PersonalData, error) {
	if s.PersonalData == nil {
		return nil, errors.New("account: personal data repository is not configured")
	}
	return s.PersonalData, nil
}

// ExportSubject собирает данные субъекта по почте; запрос записывается в журнал действий.
func (s *Service) ExportSubject(ctx context.Context, email string, operatorID uuid.UUID) (*SubjectData, error) {
	const op = "Account.ExportSubject"
	pd, err := s.personalData()
	if err != nil {
		return nil, err
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: &operatorID})
	var out *SubjectData
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		u, err := s.Users.GetByEmail(ctx, domain.NormalizeEmail(email))
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("user not found")
		}
		if err != nil {
			return err
		}
		out = &SubjectData{User: *u}
		org, err := s.Organizations.GetByUser(ctx, u.ID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
		case err != nil:
			return err
		default:
			out.Organization = org
			if out.Members, err = pd.MemberCount(ctx, org.ID); err != nil {
				return err
			}
		}
		if out.Acceptances, err = pd.Acceptances(ctx, u.ID); err != nil {
			return err
		}
		if out.Sessions, err = pd.SubjectSessions(ctx, u.ID); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: "personal_data.exported", ObjectType: "user", ObjectID: u.ID.String(), ActorType: audit.ActorAdmin,
			Data: map[string]any{"organization": out.Organization != nil, "acceptances": len(out.Acceptances),
				"sessions": len(out.Sessions)},
		})
	})
	if err != nil {
		s.log.WarnContext(ctx, "personal data export failed", logx.KeyOp, op, "operator_user_id", operatorID, logx.KeyError, err)
		return nil, err
	}
	s.log.InfoContext(ctx, "personal data exported", logx.KeyOp, op, "user_id", out.User.ID, "operator_user_id", operatorID)
	return out, nil
}

// AnonymizeSubject обезличивает субъекта по почте (06 §8): почта заменяется, учётная запись
// блокируется, сессии завершаются с удалением IP и браузера, коды входа удаляются; у организации,
// где субъект — единственный участник, стираются контактное лицо и телефон. ИНН, наименование,
// договоры, проводки, принятие документов (с IP) и журнал действий сохраняются как данные учёта.
func (s *Service) AnonymizeSubject(ctx context.Context, email string, operatorID uuid.UUID, reason string) (*AnonymizeResult, error) {
	const op = "Account.AnonymizeSubject"
	pd, err := s.personalData()
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, apperr.Validation("reason", "required")
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: &operatorID})
	res := &AnonymizeResult{}
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		found, err := s.Users.GetByEmail(ctx, domain.NormalizeEmail(email))
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("user not found")
		}
		if err != nil {
			return err
		}
		u, err := s.Users.GetForUpdate(ctx, found.ID)
		if err != nil {
			return err
		}
		res.UserID = u.ID
		oldEmail := u.Email
		now := s.Clock.Now()
		if res.Changed, err = u.Anonymize(now); err != nil || !res.Changed {
			return err
		}
		if err := pd.SaveAnonymized(ctx, u); err != nil {
			return err
		}
		if s.MAXBot != nil {
			if err := s.MAXBot.ExpireConnections(ctx, u.ID); err != nil {
				return err
			}
			if err := s.MAXBot.Disconnect(ctx, u.ID); err != nil {
				return err
			}
		}
		if res.SessionsCleared, err = pd.ClearSessions(ctx, u.ID, now); err != nil {
			return err
		}
		if res.CodesDeleted, err = pd.DeleteLoginCodes(ctx, oldEmail); err != nil {
			return err
		}
		org, err := s.Organizations.GetByUser(ctx, u.ID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
		case err != nil:
			return err
		default:
			res.OrganizationID = &org.ID
			n, err := pd.MemberCount(ctx, org.ID)
			if err != nil {
				return err
			}
			if n == 1 {
				if err := pd.ClearOrganizationContacts(ctx, org.ID, now); err != nil {
					return err
				}
				res.OrganizationCleared = true
			}
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: "user.anonymized", ObjectType: "user", ObjectID: u.ID.String(), ActorType: audit.ActorAdmin, Reason: &reason,
			Data: map[string]any{"sessions_cleared": res.SessionsCleared, "codes_deleted": res.CodesDeleted,
				"organization_cleared": res.OrganizationCleared},
		})
	})
	if err != nil {
		s.logRejected(ctx, op, err)
		return nil, err
	}
	if !res.Changed {
		s.log.InfoContext(ctx, "personal data already anonymized", logx.KeyOp, op, "user_id", res.UserID)
		return res, nil
	}
	s.log.WarnContext(ctx, "personal data anonymized", logx.KeyOp, op, "user_id", res.UserID, "operator_user_id", operatorID,
		"organization_cleared", res.OrganizationCleared, "sessions_cleared", res.SessionsCleared)
	return res, nil
}
