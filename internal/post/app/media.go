package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// MediaView — файл медиатеки со ссылкой на превью.
type MediaView struct {
	File domain.MediaFile
	// PreviewURL — подписанная ссылка на превью; пока оно строится — на оригинал (R-6).
	PreviewURL string
}

// MediaPage — страница медиатеки; NextCursor пуст на последней странице.
type MediaPage struct {
	Items      []MediaView
	NextCursor string
}

// previewParams — параметры превью: без реквизитов.
var previewParams = domain.NewDerivativeParams(domain.KindPreview, "", "")

// Upload загружает изображение (R-2, R-3): лимит частоты, проверка по содержимому,
// повтор того же файла — существующая запись. Оригинал кладётся в хранилище до
// транзакции; в транзакции — запись, превью к построению, журнал действий.
func (s *Service) Upload(ctx context.Context, orgID, userID uuid.UUID, name string, data []byte) (MediaView, error) {
	const op = "Post.UploadMedia"
	now := s.Clock.Now()
	if s.Uploads != nil {
		n, err := s.Uploads.CountByUserSince(ctx, actionMediaUploaded, userID, now.Add(-time.Hour))
		if err != nil {
			return MediaView{}, err
		}
		if n >= s.Config.UploadsPerHour {
			s.log.WarnContext(ctx, "media upload rate limited", logx.KeyOp, op, "limit", s.Config.UploadsPerHour)
			return MediaView{}, apperr.RateLimited(mediaUploadRateLimitScope, time.Hour)
		}
	}

	info := s.Renderer.Probe(data)
	if err := domain.CheckUpload(info, s.Config.Upload); err != nil {
		var re *domain.RuleError
		reason := ""
		if errors.As(err, &re) {
			reason = re.Reason
		}
		s.log.WarnContext(ctx, "media upload rejected", logx.KeyOp, op, "reason", reason,
			"content_type", info.ContentType, "size_bytes", info.Size)
		return MediaView{}, ruleError(err)
	}

	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if existing, ok, err := s.MediaFiles.FindBySHA256(ctx, orgID, sha); err != nil {
		return MediaView{}, err
	} else if ok {
		s.log.InfoContext(ctx, "media upload deduplicated", logx.KeyOp, op, KeyMediaFileID, existing.ID)
		return s.mediaView(ctx, existing)
	}

	mime := info.Mime()
	w, h := info.Dimensions()
	f := domain.MediaFile{
		ID: ids.New(), OrganizationID: orgID, Mime: mime, SizeBytes: int64(len(data)), Width: w, Height: h,
		SHA256: sha, OriginalName: domain.CleanOriginalName(name), CreatedBy: userID, CreatedAt: now,
	}
	f.StorageKey = mediaKeyArea + "/" + ids.New().String() + domain.Ext(mime)
	// Внешний вызов — до транзакции: она не держится открытой на время загрузки (07 §1).
	if err := s.Storage.PutPrivate(ctx, f.StorageKey, bytes.NewReader(data), int64(len(data)), mime); err != nil {
		return MediaView{}, err
	}

	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.MediaFiles.Insert(ctx, f); err != nil {
			return err
		}
		if err := s.ensureDerivative(ctx, orgID, f.ID, previewParams); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionMediaUploaded, ObjectType: "media_file", ObjectID: f.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"mime": mime, "size_bytes": f.SizeBytes, "width": w, "height": h, "sha256": sha},
		})
	})
	if err != nil {
		s.deleteOrphan(ctx, op, f.StorageKey)
		return MediaView{}, err
	}
	s.log.InfoContext(ctx, "media uploaded", logx.KeyOp, op, KeyMediaFileID, f.ID, "mime", mime,
		"size_bytes", f.SizeBytes, "width", w, "height", h)
	return s.mediaView(ctx, f)
}

// deleteOrphan удаляет объект, запись о котором не сохранилась.
func (s *Service) deleteOrphan(ctx context.Context, op, key string) {
	if err := s.Storage.Delete(context.WithoutCancel(ctx), key); err != nil {
		s.log.WarnContext(ctx, "media orphan object delete failed", logx.KeyOp, op, "key", key, logx.KeyError, err.Error())
	}
}

// List — живые файлы организации, новые сверху (R-21).
func (s *Service) List(ctx context.Context, orgID uuid.UUID, cursorStr string, limit int) (MediaPage, error) {
	after, err := cursor(cursorStr)
	if err != nil {
		return MediaPage{}, err
	}
	limit = pageLimit(limit)
	files, err := s.MediaFiles.List(ctx, orgID, after, limit+1)
	if err != nil {
		return MediaPage{}, err
	}
	var page MediaPage
	if len(files) > limit {
		files = files[:limit]
		last := files[limit-1]
		page.NextCursor = domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	page.Items, err = s.mediaViews(ctx, files)
	return page, err
}

// Delete мягко удаляет файл и убирает его из черновиков всех кампаний организации (R-15).
// Версии поста не меняются.
func (s *Service) Delete(ctx context.Context, orgID, userID, id uuid.UUID) error {
	const op = "Post.DeleteMedia"
	var drafts int
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Posts.LockDraftMedia(ctx, orgID); err != nil {
			return err
		}
		now := s.Clock.Now()
		if err := s.MediaFiles.SoftDelete(ctx, orgID, id, now); err != nil {
			return notFound(err, "media file")
		}
		updated, err := s.Posts.RemoveMediaFromDrafts(ctx, orgID, id, now)
		if err != nil {
			return err
		}
		drafts = len(updated)
		for _, p := range updated {
			if err := s.ensureDraftDerivatives(ctx, orgID, p.Draft); err != nil {
				return err
			}
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionMediaDeleted, ObjectType: "media_file", ObjectID: id.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"drafts_updated": drafts},
		})
	})
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "media deleted", logx.KeyOp, op, KeyMediaFileID, id, "drafts_updated", drafts)
	return nil
}

func (s *Service) mediaView(ctx context.Context, f domain.MediaFile) (MediaView, error) {
	views, err := s.mediaViews(ctx, []domain.MediaFile{f})
	if err != nil {
		return MediaView{}, err
	}
	return views[0], nil
}

// mediaViews подписывает ссылки на превью одним запросом производных на все файлы.
func (s *Service) mediaViews(ctx context.Context, files []domain.MediaFile) ([]MediaView, error) {
	fileIDs := make([]uuid.UUID, 0, len(files))
	for _, f := range files {
		fileIDs = append(fileIDs, f.ID)
	}
	previews, err := s.Derivatives.ListByFiles(ctx, fileIDs, domain.KindPreview)
	if err != nil {
		return nil, err
	}
	hash := previewParams.Hash()
	ready := make(map[uuid.UUID]domain.Derivative, len(previews))
	for _, d := range previews {
		if d.ParamsHash == hash && d.Ready() {
			ready[d.MediaFileID] = d
		}
	}
	out := make([]MediaView, 0, len(files))
	for _, f := range files {
		key := f.StorageKey
		if d, ok := ready[f.ID]; ok {
			key = d.StorageKey
		}
		u, err := s.Storage.PresignGet(ctx, key, s.Config.URLTTL)
		if err != nil {
			return nil, err
		}
		out = append(out, MediaView{File: f, PreviewURL: u})
	}
	return out, nil
}
