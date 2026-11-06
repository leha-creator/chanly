package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Тип лица организации (account, 04 §9) — на языке модуля маркировки.
const (
	LegalEntity    = "legal_entity"
	SoleProprietor = "sole_proprietor"
	SelfEmployed   = "self_employed"
)

// Тип контрагента в ОРД (OrganizationType).
const (
	OrdTypeUL = "ul"
	OrdTypeIP = "ip"
	OrdTypeFL = "fl"
)

// maxOrganizationName — предел name в ОРД.
const maxOrganizationName = 255

// OrgDetails — реквизиты организации для ОРД (порт Organizations).
type OrgDetails struct {
	VATRate    string
	ID         uuid.UUID
	LegalType  string
	INN        string
	Name       string
	KPP        string
	IsPlatform bool
}

// Organization — контрагент в ОРД (04 §6). В ОРД идентифицируется organization_id (R-1).
type Organization struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	// IsRr — организация-блогер (рекламораспространитель, R-6).
	IsRr      bool
	Sync      SyncState
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Ref — ссылка для заданий.
func (o Organization) Ref() ObjectRef { return ObjectRef{Type: TypeOrganization, ID: o.ID} }

// ExternalID — идентификатор в ОРД.
func (o Organization) ExternalID() string { return o.OrganizationID.String() }

// OrganizationRequest — тело POST /organization (API v8).
type OrganizationRequest struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	IsOrs bool   `json:"isOrs"`
	IsRr  bool   `json:"isRr"`
	INN   string `json:"inn,omitempty"`
	Name  string `json:"name,omitempty"`
	KPP   string `json:"kpp,omitempty"`
}

// OrdOrganizationType — тип лица → тип контрагента ОРД (R-6, 05 §3.3).
func OrdOrganizationType(legalType string) (string, error) {
	switch legalType {
	case LegalEntity:
		return OrdTypeUL, nil
	case SoleProprietor:
		return OrdTypeIP, nil
	case SelfEmployed:
		return OrdTypeFL, nil
	}
	return "", fmt.Errorf("%w: unknown legal type %q", ErrPermanent, legalType)
}

// NewOrganizationRequest собирает запрос контрагента (R-5, R-6). Платформа — isRr = false,
// isOrs = false; блогер — isRr = true. КПП — только у юридического лица. Наименование
// длиннее 255 символов — ErrNameTooLong (обрезка не делается).
func NewOrganizationRequest(org OrgDetails, isRr bool) (OrganizationRequest, error) {
	typ, err := OrdOrganizationType(org.LegalType)
	if err != nil {
		return OrganizationRequest{}, err
	}
	if utf8.RuneCountInString(org.Name) > maxOrganizationName {
		return OrganizationRequest{}, ErrNameTooLong
	}
	req := OrganizationRequest{
		ID: org.ID.String(), Type: typ, IsOrs: false, IsRr: isRr && !org.IsPlatform,
		INN: org.INN, Name: org.Name,
	}
	if typ == OrdTypeUL {
		req.KPP = org.KPP
	}
	return req, nil
}

// PayloadHash — SHA-256 канонического JSON запроса (R-6): ключи объектов по порядку, без пробелов.
func PayloadHash(req any) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("payload hash: %w", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("payload hash: %w", err)
	}
	canonical, err := json.Marshal(v) // map[string]any сериализуется с ключами по порядку
	if err != nil {
		return "", fmt.Errorf("payload hash: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
