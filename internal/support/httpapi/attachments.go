package httpapi

import (
	"context"
	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/support/app"
	"github.com/leha-creator/chanly/internal/support/domain"
	"io"
	"net/http"
	"os"
	"time"
)

func (h *Handlers) UploadSupportAttachment(ctx context.Context, req apigen.UploadSupportAttachmentRequestObject) (apigen.UploadSupportAttachmentResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	if !h.Svc.Enabled {
		return nil, apperr.New(apperr.CodeServiceUnavailable, "support is disabled")
	}
	if req.Body == nil {
		return nil, apperr.Validation("file", "required")
	}
	temp, e := os.CreateTemp("", "support-http-*")
	if e != nil {
		return nil, e
	}
	defer temp.Close()
	defer os.Remove(temp.Name())
	kind, name := "", ""
	seen := false
	for {
		part, e := req.Body.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, apperr.Validation("file", "malformed")
		}
		switch part.FormName() {
		case "type":
			b, e := io.ReadAll(io.LimitReader(part, 32))
			if e != nil {
				return nil, apperr.Validation("type", "invalid")
			}
			kind = string(b)
		case "file":
			if seen {
				return nil, apperr.Validation("file", "too_many")
			}
			seen = true
			name = part.FileName()
			n, e := io.Copy(temp, io.LimitReader(part, (250<<20)+1))
			if e != nil || n > 250<<20 {
				return nil, apperr.Validation("file", "too_large")
			}
		default:
			return nil, apperr.Validation("file", "unexpected_field")
		}
		part.Close()
	}
	if !seen {
		return nil, apperr.Validation("file", "required")
	}
	a, e := h.Svc.Stage(ctx, p.UserID, kind, name, temp.Name())
	return apigen.UploadSupportAttachment201JSONResponse(attachment(a)), e
}

type fileResponse struct {
	ctx         context.Context
	svc         *app.Service
	attachment  domain.Attachment
	rangeHeader string
}

func (f fileResponse) VisitGetSupportAttachmentResponse(w http.ResponseWriter) error {
	body, e := f.svc.OpenDownload(f.ctx, f.attachment)
	if e != nil {
		return e
	}
	defer body.Close()
	w.Header().Set("Content-Type", f.attachment.MIME)
	w.Header().Set("Content-Disposition", app.AttachmentDisposition(f.attachment))
	w.Header().Set("Cache-Control", "private, no-store")
	r, _ := http.NewRequestWithContext(f.ctx, http.MethodGet, "http://private.local/", nil)
	if f.rangeHeader != "" {
		r.Header.Set("Range", f.rangeHeader)
	}
	http.ServeContent(w, r, f.attachment.Name, time.Time{}, body)
	return nil
}
func (h *Handlers) GetSupportAttachment(ctx context.Context, req apigen.GetSupportAttachmentRequestObject) (apigen.GetSupportAttachmentResponseObject, error) {
	ctx, p, e := h.admin(ctx)
	if e != nil {
		return nil, e
	}
	a, e := h.Svc.Attachment(ctx, req.Id, p.UserID)
	if e != nil {
		return nil, e
	}
	if a.Status != "ready" {
		return nil, apperr.Conflict("attachment is not ready")
	}
	return fileResponse{ctx, h.Svc, a, value(req.Params.Range)}, nil
}
