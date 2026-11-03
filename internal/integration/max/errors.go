package max

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leha-creator/chanly/internal/platform/outbound"
)

// APIError — ошибка в теле ответа MAX: {"code": "...", "message": "..."}.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return fmt.Sprintf("max: %s: %s", e.Code, e.Message) }

// validateSuccess — ответ 200 может означать отказ: success=false (05 §1, 05 §2.3).
func validateSuccess(r outbound.Response) error {
	var b struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Body, &b); err != nil {
		return nil // тело не объект с success — проверяет метод
	}
	if b.Success != nil && !*b.Success {
		if b.Message == "" {
			b.Message = "success=false"
		}
		return errors.New(b.Message)
	}
	return nil
}

// AsAPIError достаёт код ошибки MAX из отказа outbound.
func AsAPIError(err error) (*APIError, bool) {
	var rej *outbound.RejectedError
	if !errors.As(err, &rej) || rej.Body == "" {
		return nil, false
	}
	var e APIError
	if json.Unmarshal([]byte(rej.Body), &e) != nil || e.Code == "" {
		return nil, false
	}
	return &e, true
}
