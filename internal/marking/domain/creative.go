package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Поля креатива по спецификации v8 (R-8).
const (
	CreativeTypeCreative  = "creative"
	FormTextBlock         = "text-block"
	FormTextGraphicBlock  = "text-graphic-block"
	MediaFileTypeImage    = "image"
	maxCreativeText       = 65000
	VersionStatusApproved = "approved"
)

// ErirStatus — подтверждение креатива ЕРИР (04 §9).
type ErirStatus string

const (
	ErirPending ErirStatus = "pending"
	ErirSuccess ErirStatus = "success"
	ErirError   ErirStatus = "error"
)

// CreativeContent — версия поста для креатива (порт CreativeContent, реализация — post/app).
type CreativeContent struct {
	VersionID uuid.UUID
	// VersionStatus — состояние версии: креатив отправляется только для approved (R-11).
	VersionStatus string
	// OrganizationID — рекламодатель.
	OrganizationID uuid.UUID
	// Text — текст body без разметки (post/domain.PlainText).
	Text string
	// URLs — адреса ссылок body по порядку, уже с UTM (post/domain.LinkURLs).
	URLs []string
	// ImageURLs — публичные адреса изображений версии по порядку; пустая строка — производная
	// ещё не в публичной области (R-9).
	ImageURLs []string
}

// PendingImages — сколько изображений ещё без публичного адреса (R-9).
func (c CreativeContent) PendingImages() int {
	n := 0
	for _, u := range c.ImageURLs {
		if u == "" {
			n++
		}
	}
	return n
}

// Creative — креатив версии поста (04 §6, D-07). В ОРД идентифицируется id записи (R-1).
type Creative struct {
	ID            uuid.UUID
	PostVersionID uuid.UUID
	// ContractID — изначальный договор; uuid.Nil — ещё не определён.
	ContractID     uuid.UUID
	Erid           string
	EridReceivedAt time.Time
	ErirStatus     ErirStatus
	ErirError      string
	Sync           SyncState
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewCreative — креатив версии, ожидающий отправки.
func NewCreative(id, versionID uuid.UUID, now time.Time) Creative {
	return Creative{ID: id, PostVersionID: versionID, ErirStatus: ErirPending, Sync: NewSyncState(), CreatedAt: now, UpdatedAt: now}
}

// Ref — ссылка для заданий.
func (c Creative) Ref() ObjectRef { return ObjectRef{Type: TypeCreative, ID: c.ID} }

// ExternalID — идентификатор в ОРД.
func (c Creative) ExternalID() string { return c.ID.String() }

// HasErid — токен получен: креатив больше не отправляется (D-03, R-8).
func (c Creative) HasErid() bool { return c.Erid != "" }

// SetErid записывает токен; false — токен уже был или пуст.
func (c *Creative) SetErid(token string, now time.Time) bool {
	if c.HasErid() || token == "" {
		return false
	}
	c.Erid = token
	c.EridReceivedAt = now
	return true
}

// ApplyErir переносит итог синхронизации в erir_status (R-3, R-12).
func (c *Creative) ApplyErir(o Outcome) {
	switch o {
	case OutcomeSynced:
		c.ErirStatus = ErirSuccess
		c.ErirError = ""
	case OutcomeFailed:
		c.ErirStatus = ErirError
		c.ErirError = c.Sync.Error
	}
}

// CreativeRequest — тело POST /creative (API v8).
type CreativeRequest struct {
	ID            string          `json:"id"`
	CreativeType  string          `json:"creativeType"`
	IsSocial      bool            `json:"isSocial"`
	IsSocialQuota bool            `json:"isSocialQuota"`
	CoBranding    bool            `json:"coBranding"`
	ContractIDs   []string        `json:"contractIds"`
	KKTUCodes     []string        `json:"kktuCodes"`
	Description   string          `json:"description,omitempty"`
	Form          string          `json:"form"`
	TextData      []string        `json:"textData,omitempty"`
	MediaData     []CreativeMedia `json:"mediaData,omitempty"`
	URLs          []string        `json:"urls,omitempty"`
}

// CreativeMedia — изображение креатива.
type CreativeMedia struct {
	MediaURL         string `json:"mediaUrl"`
	MediaURLFileType string `json:"mediaUrlFileType"`
}

// NewCreativeRequest собирает запрос креатива (R-8): не социальная реклама, без сорекламы —
// ровно один договор (изначальный), один код ККТУ из конфигурации (A-42, Q-39). Форма —
// text-graphic-block при изображениях, иначе text-block (уточнение 05 §3.4).
// Описание объекта рекламирования берётся из текста; API v8 ограничивает его 1000 символами.
// Для ККТУ 30.15.1 оно обязательно.
func NewCreativeRequest(c Creative, contractExternalID string, content CreativeContent, kktu string) CreativeRequest {
	req := CreativeRequest{
		ID: c.ExternalID(), CreativeType: CreativeTypeCreative,
		ContractIDs: []string{contractExternalID}, KKTUCodes: []string{kktu},
		Form: FormTextBlock, URLs: content.URLs,
	}
	if text := strings.TrimSpace(truncate(strings.TrimSpace(content.Text), maxCreativeText)); text != "" {
		req.TextData = []string{text}
		req.Description = strings.TrimSpace(truncate(text, 1000))
	}
	for _, u := range content.ImageURLs {
		req.MediaData = append(req.MediaData, CreativeMedia{MediaURL: u, MediaURLFileType: MediaFileTypeImage})
	}
	if len(req.MediaData) > 0 {
		req.Form = FormTextGraphicBlock
	}
	return req
}
