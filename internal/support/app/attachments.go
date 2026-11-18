package app

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/storage"
	"github.com/leha-creator/chanly/internal/support/domain"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type MediaProcessor interface {
	Prepare(context.Context, string, string, int64) (string, string, float64, int, int, error)
}

type mediaSourceError struct {
	reason string
	status int
}

func (e *mediaSourceError) Error() string { return "support media source: " + e.reason }

func cleanName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if len(name) > 180 {
		name = "attachment"
	}
	if name == "" || name == "." {
		name = "attachment"
	}
	return name
}
func sniff(path, kind string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", errors.New("media unavailable")
	}
	defer f.Close()
	b := make([]byte, 512)
	n, _ := f.Read(b)
	m := http.DetectContentType(b[:n])
	if kind == "image" && !strings.HasPrefix(m, "image/") {
		return "", apperr.Validation("file", "unsupported_type")
	}
	if (kind == "audio" || kind == "video") && (strings.HasPrefix(m, "text/") || strings.Contains(m, "html") || strings.Contains(m, "xml")) {
		return "", apperr.Validation("file", "unsupported_type")
	}
	return m, nil
}

func (s *Service) Stage(ctx context.Context, actor uuid.UUID, kind, name, path string) (domain.Attachment, error) {
	if !s.Enabled {
		return domain.Attachment{}, apperr.New(apperr.CodeServiceUnavailable, "support is disabled")
	}
	limit := s.Limits.Bytes(kind)
	stat, e := os.Stat(path)
	if e != nil {
		return domain.Attachment{}, errors.New("media staging unavailable")
	}
	if limit == 0 || stat.Size() <= 0 || stat.Size() > limit {
		return domain.Attachment{}, apperr.Validation("file", "too_large")
	}
	m, e := sniff(path, kind)
	if e != nil {
		return domain.Attachment{}, e
	}
	a := domain.Attachment{ID: ids.New(), ActorID: &actor, Type: kind, MIME: m, Size: stat.Size(), Name: cleanName(name), Status: "ready", StorageKey: storage.NewKey("support", ".bin")}
	if kind == "audio" || kind == "video" {
		a.Status = "pending"
	}
	f, e := os.Open(path)
	if e != nil {
		return a, errors.New("media staging unavailable")
	}
	e = s.Storage.PutPrivate(ctx, a.StorageKey, f, a.Size, a.MIME)
	f.Close()
	if e != nil {
		return a, errors.New("support storage unavailable")
	}
	e = s.Tx.InTx(ctx, func(ctx context.Context) error {
		if e := s.Repo.SaveAttachment(ctx, a); e != nil {
			return safeError(e)
		}
		if a.Status == "pending" {
			return s.Jobs.Enqueue(ctx, job.Job{Kind: MediaKind, DedupeKey: "support_media:" + a.ID.String(), Payload: Payload{a.ID}, MaxAttempts: 5})
		}
		return nil
	})
	if e != nil {
		_ = s.Storage.Delete(ctx, a.StorageKey)
		return a, e
	}
	s.Log.InfoContext(ctx, "support attachment staged", "attachment_id", a.ID, "type", kind, "size", a.Size)
	return a, nil
}
func (s *Service) Attachment(ctx context.Context, id, actor uuid.UUID) (domain.Attachment, error) {
	a, e := s.Repo.Attachment(ctx, id)
	if e != nil {
		return a, safeError(e)
	}
	if a.MessageID == nil && (a.ActorID == nil || *a.ActorID != actor) {
		return domain.Attachment{}, apperr.NotFound("attachment not found")
	}
	return a, nil
}

func (s *Service) PrepareMedia(ctx context.Context, id uuid.UUID) error {
	a, e := s.Repo.Attachment(ctx, id)
	if e != nil {
		return safeError(e)
	}
	if a.Status != "pending" && a.Status != "processing" {
		return nil
	}
	claimed, e := s.Repo.ClaimMedia(ctx, id, s.Clock.Now())
	if e != nil {
		return safeError(e)
	}
	if !claimed {
		return job.Postpone(errors.New("support media is being prepared"), time.Minute)
	}
	temp, e := os.CreateTemp("", "support-source-*")
	if e != nil {
		return errors.New("support media staging failed")
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	limit := s.Limits.Bytes(a.Type)
	e = s.copySource(ctx, a, temp, limit)
	if e != nil {
		reason, status := "source_unavailable", 0
		var source *mediaSourceError
		if errors.As(e, &source) {
			reason, status = source.reason, source.status
		}
		s.Log.WarnContext(ctx, "[FIX] support media source failed", "attachment_id", a.ID, "type", a.Type, "reason", reason, "http_status", status)
		return s.mediaFailure(ctx, a, "failed")
	}
	stat, _ := temp.Stat()
	a.Size = stat.Size()
	a.MIME, e = sniff(temp.Name(), a.Type)
	if e != nil {
		return s.mediaFailure(ctx, a, "unsupported")
	}
	if a.StorageKey == "" {
		if _, e = temp.Seek(0, io.SeekStart); e != nil {
			return e
		}
		a.StorageKey = storage.NewKey("support", ".bin")
		if e = s.Storage.PutPrivate(ctx, a.StorageKey, temp, a.Size, a.MIME); e != nil {
			return s.mediaFailure(ctx, a, "failed")
		}
	}
	if a.Type == "audio" || a.Type == "video" {
		prepared, m, d, w, h, e := s.Media.Prepare(ctx, a.Type, temp.Name(), limit)
		if e != nil {
			return s.mediaFailure(ctx, a, "failed")
		}
		defer os.Remove(prepared)
		f, e := os.Open(prepared)
		if e != nil {
			return s.mediaFailure(ctx, a, "failed")
		}
		defer f.Close()
		stat, _ := f.Stat()
		a.OriginalKey = a.StorageKey
		a.StorageKey = storage.NewKey("support", filepath.Ext(prepared))
		a.Size = stat.Size()
		a.MIME = m
		a.Duration = d
		a.Width = w
		a.Height = h
		a.Name = strings.TrimSuffix(cleanName(a.Name), filepath.Ext(a.Name)) + filepath.Ext(prepared)
		if e = s.Storage.PutPrivate(ctx, a.StorageKey, f, a.Size, a.MIME); e != nil {
			return s.mediaFailure(ctx, a, "failed")
		}
	}
	a.Status = "ready"
	a.ProviderURL = ""
	a.ProviderToken = ""
	if e = s.Repo.SaveAttachment(ctx, a); e != nil {
		return safeError(e)
	}
	s.Log.InfoContext(ctx, "support media ready", "attachment_id", id, "type", a.Type, "size", a.Size)
	return nil
}
func (s *Service) mediaFailure(ctx context.Context, a domain.Attachment, status string) error {
	a.Status = status
	s.Log.WarnContext(ctx, "support media preparation failed", "attachment_id", a.ID, "status", status)
	return safeError(s.Repo.SaveAttachment(ctx, a))
}
func (s *Service) copySource(ctx context.Context, a domain.Attachment, w io.Writer, limit int64) error {
	if limit <= 0 {
		return &mediaSourceError{reason: "unsupported_type"}
	}
	var r io.ReadCloser
	if a.StorageKey != "" {
		body, _, e := s.Storage.Get(ctx, a.StorageKey)
		if e != nil {
			return &mediaSourceError{reason: "storage_read_failed"}
		}
		r = body
	} else {
		u := a.ProviderURL
		if u == "" && a.Type == "video" && a.ProviderToken != "" {
			var e error
			u, e = s.Provider.ResolveVideo(ctx, a.ProviderToken)
			if e != nil {
				return &mediaSourceError{reason: "video_lookup_failed"}
			}
		}
		if e := outbound.PublicMediaURL(u, s.MediaHosts); e != nil {
			return &mediaSourceError{reason: "origin_not_allowed"}
		}
		s.Log.DebugContext(ctx, "[FIX] support media origin allowed", "attachment_id", a.ID, "type", a.Type)
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if e != nil {
			return &mediaSourceError{reason: "invalid_source"}
		}
		resp, e := s.Fetch.Do(req)
		if e != nil {
			return &mediaSourceError{reason: "download_failed"}
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return &mediaSourceError{reason: "http_rejected", status: resp.StatusCode}
		}
		if resp.ContentLength > limit {
			resp.Body.Close()
			return &mediaSourceError{reason: "too_large"}
		}
		r = resp.Body
	}
	defer r.Close()
	n, e := io.Copy(w, io.LimitReader(r, limit+1))
	if e != nil || n > limit {
		return &mediaSourceError{reason: "download_incomplete_or_too_large"}
	}
	return nil
}

// OpenDownload returns a private seekable temporary file for net/http ServeContent.
type Download struct {
	io.ReadSeeker
	close func() error
}

func (d *Download) Close() error { return d.close() }
func (s *Service) OpenDownload(ctx context.Context, a domain.Attachment) (*Download, error) {
	if a.Status != "ready" {
		return nil, apperr.Conflict("attachment is not ready")
	}
	r, _, e := s.Storage.Get(ctx, a.StorageKey)
	if e != nil {
		return nil, errors.New("support storage unavailable")
	}
	if seek, ok := r.(io.ReadSeeker); ok {
		return &Download{ReadSeeker: seek, close: r.Close}, nil
	}
	defer r.Close()
	f, e := os.CreateTemp("", "support-download-*")
	if e != nil {
		return nil, e
	}
	n, e := io.Copy(f, io.LimitReader(r, a.Size+1))
	if e != nil || n != a.Size {
		f.Close()
		os.Remove(f.Name())
		return nil, errors.New("support download incomplete")
	}
	_, e = f.Seek(0, io.SeekStart)
	if e != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, e
	}
	return &Download{ReadSeeker: f, close: func() error { e := f.Close(); _ = os.Remove(f.Name()); return e }}, nil
}
func AttachmentDisposition(a domain.Attachment) string {
	kind := "inline"
	if a.Type == "file" {
		kind = "attachment"
	}
	return mime.FormatMediaType(kind, map[string]string{"filename": cleanName(a.Name)})
}
