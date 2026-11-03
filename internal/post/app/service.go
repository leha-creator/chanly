// Package app — сценарии модуля post: медиатека и производные изображений,
// кампании, черновик поста и предпросмотр (S-04, S-05), версии поста для
// следующих этапов (R-18), публикация производных и очистка хранилища.
// Транзакции, задания, журнал действий и журнал приложения — здесь; правила — в domain.
package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// Поля журнала (07 §5.1).
const (
	KeyMediaFileID  = "media_file_id"
	KeyDerivativeID = "derivative_id"
	KeyCampaignID   = "campaign_id"
	KeyPostID       = "post_id"
	KeyVersionID    = "version_id"
)

// Виды заданий модуля (07 §2, R-19).
const (
	KindBuildMediaDerivative   = "build_media_derivative"
	KindPublishMediaDerivative = "publish_media_derivative"
	KindPurgeMedia             = "purge_media"
)

// Попытки заданий (07 §2, R-19).
const (
	buildAttempts   = 5
	publishAttempts = 10
)

// Действия журнала действий.
const (
	actionMediaUploaded        = "media.uploaded"
	actionMediaDeleted         = "media.deleted"
	actionCampaignCreated      = "campaign.created"
	actionCampaignUpdated      = "campaign.updated"
	actionPostDraftSaved       = "post.draft_saved"
	actionPostVersionCreated   = "post.version_created"
	actionPostVersionApproved  = "post.version_approved"
	actionPostVersionRejected  = "post.version_rejected"
	actionPostVersionReady     = "post.version_ready"
	actionCampaignOrdered      = "campaign.ordered"
	actionCampaignArchived     = "campaign.archived"
	purgeBatch                 = 100
	defaultPageLimit           = 20
	maxPageLimit               = 100
	reasonUnsupportedType      = "unsupported_type"
	derivativeKeyArea          = "derivative"
	mediaKeyArea               = "media"
	problemsDetail             = "problems"
	reasonPostNotReady         = "post_not_ready"
	reasonNotDraft             = "not_draft"
	reasonArchived             = "campaign_archived"
	mediaUploadRateLimitScope  = "user"
	derivativeSkipAlreadyReady = "already_ready"
)

// Config — параметры модуля (07 §8); значения по умолчанию — из R-2, R-6, R-11, R-19.
type Config struct {
	Upload         domain.UploadLimits
	UploadsPerHour int
	AlbumLimit     int
	// URLTTL — срок подписанных ссылок кабинета (MEDIA_URL_TTL).
	URLTTL time.Duration
	// RetentionDeleted — через сколько после удаления файл очищается из хранилища.
	RetentionDeleted time.Duration
	BodyLimit        int
	FinalTextLimit   int
	Doc              domain.DocLimits
}

func (c *Config) defaults() {
	if c.Upload == (domain.UploadLimits{}) {
		c.Upload = domain.DefaultUploadLimits
	}
	if c.UploadsPerHour == 0 {
		c.UploadsPerHour = 60
	}
	if c.AlbumLimit == 0 {
		c.AlbumLimit = domain.DefaultAlbumLimit
	}
	if c.URLTTL == 0 {
		c.URLTTL = time.Hour
	}
	if c.RetentionDeleted == 0 {
		c.RetentionDeleted = 30 * 24 * time.Hour
	}
	if c.BodyLimit == 0 {
		c.BodyLimit = domain.DefaultBodyLimit
	}
	if c.FinalTextLimit == 0 {
		c.FinalTextLimit = domain.DefaultFinalTextLimit
	}
	if c.Doc == (domain.DocLimits{}) {
		c.Doc = domain.DefaultDocLimits
	}
}

// UploadCounter считает действия пользователя за окно — для лимита загрузок (R-2).
type UploadCounter interface {
	CountByUserSince(ctx context.Context, action string, userID uuid.UUID, since time.Time) (int, error)
}

// Deps — зависимости сервиса; связывает bootstrap.
type Deps struct {
	MediaFiles  domain.MediaFiles
	Derivatives domain.Derivatives
	Campaigns   domain.Campaigns
	Posts       domain.Posts
	Versions    domain.Versions
	Requisites  domain.AdvertiserRequisites
	// Listener — последствия смены состояния версии (R-5 этапа «Заказ и размещения»);
	// nil — NopVersionListener.
	Listener domain.VersionListener
	Renderer domain.Renderer
	Storage  domain.ObjectStore
	Uploads  UploadCounter
	Tx       tx.Runner
	Jobs     job.Enqueuer
	Audit    audit.Recorder
	Clock    clock.Clock
	Log      *slog.Logger
	Config   Config
}

// Service — сценарии модуля post.
type Service struct {
	Deps
	log *slog.Logger
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	if d.Listener == nil {
		d.Listener = NopVersionListener{Log: d.Log}
	}
	return &Service{Deps: d, log: d.Log.With(logx.KeyModule, "post")}
}

// SetVersionListener подставляет реализацию модуля размещений (bootstrap): тот собирается
// после модуля post, поэтому слушатель задаётся отдельно.
func (s *Service) SetVersionListener(l domain.VersionListener) { s.Listener = l }

// NopVersionListener — пустая реализация VersionListener без модуля размещений.
type NopVersionListener struct{ Log *slog.Logger }

func (n NopVersionListener) VersionChanged(ctx context.Context, c domain.VersionChange) error {
	n.Log.DebugContext(ctx, "post version change has no listener", logx.KeyOp, "Post.VersionListener",
		KeyVersionID, c.VersionID, "to", c.To)
	return nil
}

// ruleError переводит нарушение правила домена в 422 с details.reason.
func ruleError(err error) error {
	var re *domain.RuleError
	if errors.As(err, &re) {
		return apperr.Validation(re.Field, re.Reason).WithCause(err)
	}
	var de *domain.DocError
	if errors.As(err, &de) {
		return apperr.Validation("content_doc", domain.ReasonInvalidDoc).WithDetail("path", de.Path).
			WithDetail("rule", de.Rule).WithCause(err)
	}
	return err
}

// notFound переводит domain.ErrNotFound в 404 с сообщением what.
func notFound(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound(what + " not found")
	}
	return err
}

// pageLimit приводит размер страницы к допустимому.
func pageLimit(n int) int {
	switch {
	case n <= 0:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	}
	return n
}

// cursor разбирает курсор страницы; испорченный — 422.
func cursor(s string) (*domain.PageCursor, error) {
	c, err := domain.DecodePageCursor(s)
	if err != nil {
		return nil, ruleError(err)
	}
	return c, nil
}

// BuildPayload — полезная нагрузка build_media_derivative. Реквизиты для плашки в
// задание не кладутся: обработчик берёт текущие и сверяет хеш (R-6, R-14).
type BuildPayload struct {
	DerivativeID   uuid.UUID `json:"derivative_id"`
	OrganizationID uuid.UUID `json:"organization_id"`
}

// PublishPayload — полезная нагрузка publish_media_derivative.
type PublishPayload struct {
	DerivativeID uuid.UUID `json:"derivative_id"`
}
