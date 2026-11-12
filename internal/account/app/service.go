// Package app — сценарии модуля аккаунтов (S-01, S-10): вход по коду из
// письма, сессии, организации и реквизиты, документ о режиме налогообложения,
// оферты. Транзакции, задания, журнал действий и журнал приложения — здесь;
// правила — в domain.
package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
)

// Config — параметры модуля; значения по умолчанию — 07 §4.
type Config struct {
	LoginCodeTTL      time.Duration
	LoginCodeAttempts int
	CodesPerEmailHour int
	CodesPerIPHour    int
	SessionTTL        time.Duration
	AdminSessionTTL   time.Duration
	// Pepper — секрет HMAC кода входа (R-3).
	Pepper []byte
	// DocumentURLTTL — срок подписанной ссылки на файл оферты.
	DocumentURLTTL time.Duration
	// TaxDocumentUploadsPerHour — лимит загрузок документа на пользователя (07 §4).
	TaxDocumentUploadsPerHour int
}

// Sealer шифрует секреты полезной нагрузки задания (R-1).
type Sealer interface {
	Seal(plaintext []byte) (string, error)
	Open(sealed string) ([]byte, error)
}

// UploadCounter считает действия пользователя за окно — для лимита загрузок.
type UploadCounter interface {
	CountByUserSince(ctx context.Context, action string, userID uuid.UUID, since time.Time) (int, error)
}

// Deps — зависимости сервиса; связывает bootstrap.
type Deps struct {
	Settings      domain.AccountSettingsRepository
	Users         domain.Users
	AuthCodes     domain.AuthCodes
	Sessions      domain.Sessions
	Organizations domain.Organizations
	// Admin — организации для администратора (этап «API администратора»); nil — API не подключён.
	Admin domain.AdminOrganizations
	// PersonalData — выгрузка и обезличивание субъекта (R-26); nil — команды не подключены.
	PersonalData domain.PersonalData
	// Platform — строка организации-платформы (этап «Маркировка», R-5); nil — не нужна.
	Platform domain.PlatformOrganizations
	Legal    domain.LegalDocuments
	Storage  domain.FileStorage
	Sender   domain.LoginCodeSender
	Sealer   Sealer
	Uploads  UploadCounter
	Tx       tx.Runner
	Jobs     job.Enqueuer
	Audit    audit.Recorder
	Clock    clock.Clock
	Log      *slog.Logger
	Config   Config
}

// Service — сценарии модуля аккаунтов.
type Service struct {
	Deps
	log *slog.Logger
	// orgListener — модуль маркировки (SetOrganizationListener).
	orgListener domain.OrganizationListener
}

// New создаёт сервис.
func New(d Deps) *Service {
	if d.Config.DocumentURLTTL == 0 {
		d.Config.DocumentURLTTL = 10 * time.Minute
	}
	if d.Config.TaxDocumentUploadsPerHour == 0 {
		d.Config.TaxDocumentUploadsPerHour = 10
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "account")}
}

// Principal — кто выполняет запрос: пользователь сессии, его организация и кабинеты.
type Principal struct {
	UserID         uuid.UUID
	SessionID      uuid.UUID
	AdminRole      *domain.AdminRole
	OrganizationID *uuid.UUID
	Cabinets       []domain.Cabinet
}

// actorType — тип действующего лица для журнала действий (04 §9). Пользователь
// без организации ещё не выбрал роль; его действия записываются как действия
// рекламодателя — первой роли сценария S-01.
func (p Principal) actorType() string {
	switch {
	case p.AdminRole != nil:
		return audit.ActorAdmin
	case len(p.Cabinets) == 1 && p.Cabinets[0] == domain.CabinetBlogger:
		return audit.ActorBlogger
	default:
		return audit.ActorAdvertiser
	}
}

// organization возвращает организацию пользователя или not_found.
func (s *Service) organization(ctx context.Context, p Principal) (*domain.Organization, error) {
	o, err := s.Organizations.GetByUser(ctx, p.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("organization not found")
	}
	return o, err
}

func ptr[T any](v T) *T { return &v }
