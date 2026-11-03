package httpapi

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func (h *Handlers) UploadMedia(ctx context.Context, req apigen.UploadMediaRequestObject) (apigen.UploadMediaResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	// R-2: не больше UploadConcurrency загрузок на экземпляр одновременно; сверх предела — 429.
	select {
	case h.uploads <- struct{}{}:
		defer func() { <-h.uploads }()
	default:
		h.log.WarnContext(ctx, "media upload concurrency exceeded", logx.KeyOp, "Post.UploadMedia", "limit", cap(h.uploads))
		return nil, apperr.RateLimited("instance", time.Second)
	}
	name, data, err := h.readFile(req.Body)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Upload(ctx, org, user, name, data)
	if err != nil {
		return nil, err
	}
	return apigen.UploadMedia201JSONResponse(toMediaFile(v)), nil
}

// readFile читает часть file из multipart, не больше предела + 1 байт: лишний байт
// даёт too_large в domain.CheckUpload. Тело больше предела сервера — тоже too_large.
func (h *Handlers) readFile(r *multipart.Reader) (string, []byte, error) {
	var name string
	var data []byte
	found := false
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, partError(err)
		}
		if part.FormName() != "file" {
			continue
		}
		found = true
		name = part.FileName()
		if data, err = io.ReadAll(io.LimitReader(part, h.opts.MaxUploadBytes+1)); err != nil {
			return "", nil, partError(err)
		}
	}
	if !found {
		return "", nil, apperr.Validation("file", "required")
	}
	return name, data, nil
}

func partError(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperr.Validation("file", "too_large").WithCause(err)
	}
	return apperr.Validation("file", "malformed").WithCause(err)
}

func (h *Handlers) ListMedia(ctx context.Context, req apigen.ListMediaRequestObject) (apigen.ListMediaResponseObject, error) {
	org, _, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	page, err := h.svc.List(ctx, org, deref(req.Params.Cursor), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items := make([]apigen.MediaFile, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, toMediaFile(v))
	}
	return apigen.ListMedia200JSONResponse{Items: items, NextCursor: opt(page.NextCursor)}, nil
}

func (h *Handlers) DeleteMedia(ctx context.Context, req apigen.DeleteMediaRequestObject) (apigen.DeleteMediaResponseObject, error) {
	org, user, err := advertiser(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, org, user, req.Id); err != nil {
		return nil, err
	}
	return apigen.DeleteMedia204Response{}, nil
}
