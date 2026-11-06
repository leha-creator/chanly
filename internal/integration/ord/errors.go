package ord

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/outbound"
)

// ErrOrganizationNotFound means an authenticated lookup found no such organization.
var ErrOrganizationNotFound = errors.New("ord organization not found")

func organizationNotFound(resp outbound.Response) bool {
	var body struct {
		Detail string `json:"detail"`
	}
	return resp.Status == http.StatusNotFound && json.Unmarshal(resp.Body, &body) == nil &&
		body.Detail == "Not found organization by id"
}

// ValidationError — ОРД отклонил запрос проверкой схемы (422, HTTPValidationError).
type ValidationError struct {
	Details []string
}

func (e *ValidationError) DefiniteRejection() bool { return true }

func (e *ValidationError) Error() string {
	return "ord validation failed: " + strings.Join(e.Details, "; ")
}

// classify переводит ошибку outbound в термины планировщика (05 §1): 422 — постоянная
// ошибка с текстом из detail[] (loc: msg); прочие 4xx кроме 429 — постоянные (уже
// job.Permanent из outbound); 5xx, 429 и сеть — повтор.
func classify(resp outbound.Response, err error) error {
	if !job.IsPermanent(err) || resp.Status != http.StatusUnprocessableEntity {
		return err
	}
	if v := parseValidation(resp.Body); v != nil {
		return job.Permanent(v)
	}
	return err
}

func parseValidation(body []byte) *ValidationError {
	var b struct {
		Detail []struct {
			Loc []any  `json:"loc"`
			Msg string `json:"msg"`
		} `json:"detail"`
	}
	if json.Unmarshal(body, &b) != nil || len(b.Detail) == 0 {
		return nil
	}
	v := &ValidationError{}
	for _, d := range b.Detail {
		loc := make([]string, 0, len(d.Loc))
		for _, l := range d.Loc {
			loc = append(loc, fmt.Sprint(l))
		}
		v.Details = append(v.Details, strings.Join(loc, ".")+": "+d.Msg)
	}
	return v
}
