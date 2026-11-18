package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/support/domain"
)

const Source = "max_support"

type Recorder interface {
	Record(context.Context, inbound.Event) (bool, error)
}
type webhook struct {
	pathSecret, headerSecret, botID string
	recorder                        Recorder
	log                             *slog.Logger
}

func NewWebhook(pathSecret, headerSecret, botID string, r Recorder, log *slog.Logger) http.Handler {
	return &webhook{pathSecret, headerSecret, botID, r, log}
}
func (h *webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.pathSecret == "" || h.headerSecret == "" || subtle.ConstantTimeCompare([]byte(r.PathValue("secret")), []byte(h.pathSecret)) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Max-Bot-Api-Secret")), []byte(h.headerSecret)) != 1 {
		w.WriteHeader(401)
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if e != nil {
		w.WriteHeader(413)
		return
	}
	event, ok := parseIncoming(body, h.botID)
	if !ok {
		w.WriteHeader(200)
		return
	}
	payload, e := json.Marshal(event)
	if e != nil {
		w.WriteHeader(400)
		return
	}
	_, e = h.recorder.Record(r.Context(), inbound.Event{Source: Source, ExternalKey: inbound.KeyFromBody(body), EventType: event.Type, Payload: payload})
	if e != nil {
		h.log.ErrorContext(r.Context(), "support webhook persistence failed")
		w.WriteHeader(500)
		return
	}
	w.WriteHeader(200)
}

func parseIncoming(body []byte, botID string) (domain.Incoming, bool) {
	var u struct {
		Type      string `json:"update_type"`
		Timestamp int64  `json:"timestamp"`
		MID       string `json:"message_id"`
		Message   *struct {
			Sender *struct {
				ID       int64  `json:"user_id"`
				Name     string `json:"name"`
				Username string `json:"username"`
				IsBot    bool   `json:"is_bot"`
			} `json:"sender"`
			Recipient struct {
				Type string `json:"chat_type"`
			} `json:"recipient"`
			Timestamp int64 `json:"timestamp"`
			Body      struct {
				MID         string `json:"mid"`
				Text        string `json:"text"`
				Attachments []struct {
					Type    string `json:"type"`
					Payload struct {
						URL   string `json:"url"`
						Token string `json:"token"`
						Name  string `json:"filename"`
						Size  int64  `json:"size"`
					} `json:"payload"`
				} `json:"attachments"`
			} `json:"body"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &u) != nil {
		return domain.Incoming{}, false
	}
	in := domain.Incoming{Type: u.Type, BotID: botID, ExternalID: u.MID, At: time.UnixMilli(u.Timestamp).UTC()}
	if u.Type == "message_removed" {
		return in, in.ExternalID != ""
	}
	if u.Type != "message_created" && u.Type != "message_edited" {
		return in, false
	}
	if u.Message == nil || u.Message.Sender == nil || u.Message.Sender.IsBot || u.Message.Recipient.Type != "dialog" || u.Message.Sender.ID <= 0 {
		return in, false
	}
	m := u.Message
	in.UserID = strconv.FormatInt(m.Sender.ID, 10)
	if in.UserID == botID {
		return in, false
	}
	in.Name = m.Sender.Name
	in.Username = m.Sender.Username
	in.Text = m.Body.Text
	in.ExternalID = m.Body.MID
	if m.Timestamp > 0 {
		in.At = time.UnixMilli(m.Timestamp).UTC()
	}
	for _, a := range m.Body.Attachments {
		in.Media = append(in.Media, domain.IncomingMedia{Type: a.Type, Name: a.Payload.Name, URL: a.Payload.URL, Token: a.Payload.Token, Size: a.Payload.Size})
	}
	return in, in.ExternalID != "" && (in.Text != "" || len(in.Media) > 0 || u.Type == "message_edited")
}
