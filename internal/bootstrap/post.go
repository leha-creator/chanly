package bootstrap

import (
	"context"
	"errors"

	"github.com/google/uuid"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/tx"
	postapp "github.com/leha-creator/chanly/internal/post/app"
	postdomain "github.com/leha-creator/chanly/internal/post/domain"
	posthttp "github.com/leha-creator/chanly/internal/post/httpapi"
	"github.com/leha-creator/chanly/internal/post/imaging"
	postpg "github.com/leha-creator/chanly/internal/post/postgres"
)

// postHandlers — псевдоним для встраивания в apiImpl рядом с обработчиками других модулей.
type postHandlers = posthttp.Handlers

// postRequisites — переходник порта post/domain.AdvertiserRequisites поверх хранилища
// модуля аккаунтов (R-14): название и ИНН текущей организации.
type postRequisites struct{ orgs *accountpg.Organizations }

var _ postdomain.AdvertiserRequisites = postRequisites{}

func (r postRequisites) Requisites(ctx context.Context, orgID uuid.UUID) (postdomain.Requisites, error) {
	name, inn, err := r.orgs.Requisites(ctx, orgID)
	if errors.Is(err, accountdomain.ErrNotFound) {
		return postdomain.Requisites{}, nil
	}
	if err != nil {
		return postdomain.Requisites{}, err
	}
	return postdomain.Requisites{Name: name, INN: inn}, nil
}

// newPostService собирает модуль post: медиатека, кампании, черновик, версии.
func newPostService(cfg Config, in *infra, txRunner tx.Runner, queue job.Enqueuer, auditLog *audit.Log,
	accountStore *accountpg.Store) *postapp.Service {
	log := in.log
	m := cfg.Media
	store := postpg.New(in.pool, log.With(logx.KeyModule, "post"))
	deps := postapp.Deps{
		MediaFiles: store.MediaFiles(), Derivatives: store.Derivatives(), Campaigns: store.Campaigns(),
		Posts: store.Posts(), Versions: store.Versions(),
		Requisites: postRequisites{orgs: accountStore.Organizations()},
		Renderer: imaging.New(imaging.Options{PreviewSide: m.PreviewSide, MaxBytes: m.MaxBytes, Concurrency: m.BuildConcurrency},
			in.clock, log),
		Uploads: auditLog, Tx: txRunner, Jobs: queue, Audit: auditLog, Clock: in.clock, Log: log,
		Config: postapp.Config{
			Upload:         postdomain.UploadLimits{MaxBytes: m.MaxBytes, MinSide: m.MinSide, MaxSide: maxMediaSide},
			UploadsPerHour: m.UploadsPerHour, AlbumLimit: m.AlbumLimit, URLTTL: m.URLTTL,
			RetentionDeleted: m.RetentionDeleted, BodyLimit: m.PostBodyLimit, FinalTextLimit: m.PostFinalTextLimit,
			Doc: postdomain.DefaultDocLimits,
		},
	}
	if in.storage != nil {
		deps.Storage = in.storage
	}
	return postapp.New(deps)
}

// newPostHandlers — операции кабинета модуля post.
func newPostHandlers(cfg Config, svc *postapp.Service, in *infra) *posthttp.Handlers {
	return posthttp.New(svc, in.log, posthttp.Options{MaxUploadBytes: cfg.Media.MaxBytes, UploadConcurrency: cfg.Media.UploadConcurrency})
}
