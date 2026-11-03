package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

// VersionStatus — состояние версии поста (03 §1.1, 04 §9).
type VersionStatus string

const (
	VersionPendingModeration VersionStatus = "pending_moderation"
	VersionRejected          VersionStatus = "rejected"
	VersionApproved          VersionStatus = "approved"
	VersionReady             VersionStatus = "ready"
)

// Переходы версии (03 §1.1).
const (
	TransitionApprove   = "approve"
	TransitionReject    = "reject"
	TransitionMarkReady = "mark_ready"
)

// MaxRejectionReason — предел причины отказа в символах.
const MaxRejectionReason = 1000

var (
	ErrRejectionReason = &RuleError{Field: "reason", Reason: "invalid_length"}
	ErrEridRequired    = &RuleError{Field: "erid", Reason: ReasonRequired}
	errVersionInvalid  = errors.New("post: version is invalid")
)

// VersionMedia — изображение версии: оригинал и файл, который уходит в канал.
type VersionMedia struct {
	Position     int
	MediaFileID  uuid.UUID
	DerivativeID uuid.UUID
}

// Version — снимок поста (04 §4). После ready не меняется (D-03, I-09).
type Version struct {
	ID              uuid.UUID
	PostID          uuid.UUID
	Number          int
	Status          VersionStatus
	ContentDoc      Doc
	Body            string
	FinalText       string
	Erid            string
	TokenCarrier    TokenCarrier
	TokenPosition   TokenPosition
	AdvertiserName  string
	AdvertiserINN   string
	ModeratedBy     *uuid.UUID
	ModeratedAt     *time.Time
	RejectionReason string
	LockVersion     int
	CreatedBy       uuid.UUID
	CreatedAt       time.Time
	Media           []VersionMedia
}

// NewVersion — версия number из черновика поста в pending_moderation (R-18).
// body — сериализованный текст с UTM-метками заказа; проблемы черновика проверяет app.
func NewVersion(id uuid.UUID, p Post, number int, req Requisites, body string, media []VersionMedia,
	userID uuid.UUID, now time.Time) (Version, error) {
	if number < 1 || !req.Complete() || len(media) != len(p.Draft.MediaIDs) {
		return Version{}, errVersionInvalid
	}
	for i, m := range media {
		if m.Position != i || m.MediaFileID != p.Draft.MediaIDs[i] {
			return Version{}, errVersionInvalid
		}
	}
	return Version{
		ID: id, PostID: p.ID, Number: number, Status: VersionPendingModeration,
		ContentDoc: p.Draft.Doc, Body: body,
		TokenCarrier: p.Draft.TokenCarrier, TokenPosition: p.Draft.TokenPosition,
		AdvertiserName: req.Name, AdvertiserINN: req.INN,
		CreatedBy: userID, CreatedAt: now, Media: media,
	}, nil
}

func (v *Version) transition(name string, from VersionStatus) error {
	if v.Status != from {
		return apperr.InvalidTransition(name, string(v.Status))
	}
	return nil
}

// Approve — администратор одобрил версию (pending_moderation → approved, S-17).
func (v *Version) Approve(adminID uuid.UUID, now time.Time) error {
	if err := v.transition(TransitionApprove, VersionPendingModeration); err != nil {
		return err
	}
	v.Status, v.ModeratedBy, v.ModeratedAt = VersionApproved, &adminID, &now
	v.LockVersion++
	return nil
}

// Reject — администратор отклонил версию с причиной (pending_moderation → rejected).
func (v *Version) Reject(adminID uuid.UUID, reason string, now time.Time) error {
	if err := v.transition(TransitionReject, VersionPendingModeration); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > MaxRejectionReason {
		return ErrRejectionReason
	}
	v.Status, v.ModeratedBy, v.ModeratedAt, v.RejectionReason = VersionRejected, &adminID, &now, reason
	v.LockVersion++
	return nil
}

// MarkReady — получен erid (approved → ready): собирается итоговый текст (05 §2.5).
// Итоговый текст длиннее finalLimit — ErrFinalTextTooLong, состояние не меняется.
func (v *Version) MarkReady(erid string, finalLimit int) error {
	if err := v.transition(TransitionMarkReady, VersionApproved); err != nil {
		return err
	}
	if strings.TrimSpace(erid) == "" {
		return ErrEridRequired
	}
	final, err := FinalText(v.Body, Marking{Carrier: v.TokenCarrier, Position: v.TokenPosition,
		Name: v.AdvertiserName, INN: v.AdvertiserINN}, erid, finalLimit)
	if err != nil {
		return err
	}
	v.Status, v.Erid, v.FinalText = VersionReady, erid, final
	v.LockVersion++
	return nil
}
