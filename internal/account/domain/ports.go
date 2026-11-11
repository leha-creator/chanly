package domain

import (
	"context"
	"io"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// Порты модуля. Репозитории реализует account/postgres, остальное — адаптеры,
// которые связывает bootstrap. Все методы работают в транзакции из context, если она открыта.

// Users — пользователи.
type Users interface {
	// GetByEmail возвращает ErrNotFound, если пользователя нет.
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*User, error)
	Create(ctx context.Context, u *User) error
	SetLastLogin(ctx context.Context, id uuid.UUID, at time.Time) error
	// SetAdminRole ставит или снимает (nil) роль администратора (R-2 этапа «Каталог»).
	SetAdminRole(ctx context.Context, id uuid.UUID, role *AdminRole, at time.Time) error
	// GetForUpdate читает пользователя с блокировкой строки; ErrNotFound, если его нет.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*User, error)
	// SetBlocked сохраняет статус и основание блокировки (R-12 этапа «API администратора»).
	SetBlocked(ctx context.Context, u *User) error
}

// WindowCount — сколько записей в окне и когда появилась самая ранняя из них.
type WindowCount struct {
	Count  int
	Oldest time.Time
}

// AuthCodes — коды входа.
type AuthCodes interface {
	Create(ctx context.Context, c *LoginCode) error
	// LatestActiveForUpdate — последний непогашенный код адреса с блокировкой строки;
	// ErrNotFound, если такого нет.
	LatestActiveForUpdate(ctx context.Context, email string) (*LoginCode, error)
	// ConsumeActive гасит все непогашенные коды адреса: действует только последний.
	ConsumeActive(ctx context.Context, email string, now time.Time) error
	// Save сохраняет попытки и отметку погашения.
	Save(ctx context.Context, c *LoginCode) error
	CountByEmailSince(ctx context.Context, email string, since time.Time) (WindowCount, error)
	CountByIPSince(ctx context.Context, ip netip.Addr, since time.Time) (WindowCount, error)
}

// Sessions — сессии.
type Sessions interface {
	Create(ctx context.Context, s *Session) error
	// GetByTokenHash возвращает ErrNotFound, если сессии нет.
	GetByTokenHash(ctx context.Context, hash string) (*Session, error)
	SetExpiresAt(ctx context.Context, id uuid.UUID, expiresAt, now time.Time) error
	Revoke(ctx context.Context, id uuid.UUID, now time.Time) error
	// RevokeAllForUser завершает все действующие сессии пользователя; возвращает их число.
	RevokeAllForUser(ctx context.Context, userID uuid.UUID, now time.Time) (int64, error)
}

// Organizations — организации, участники и профили. Методы кабинета принимают
// идентификатор организации явно (07 §4).
type Organizations interface {
	// Create сохраняет организацию, её профили и участника-владельца.
	Create(ctx context.Context, o *Organization, owner uuid.UUID) error
	// GetByUser возвращает организацию, где пользователь — участник; ErrNotFound, если её нет.
	GetByUser(ctx context.Context, userID uuid.UUID) (*Organization, error)
	// GetForUpdate читает организацию с блокировкой строки.
	GetForUpdate(ctx context.Context, organizationID uuid.UUID) (*Organization, error)
	// Update сохраняет реквизиты и создаёт недостающие профили.
	Update(ctx context.Context, o *Organization) error
	INNTaken(ctx context.Context, inn string) (bool, error)
}

// LegalDocuments — оферты и их принятие.
type LegalDocuments interface {
	// Current — действующие версии по видам (наибольший version).
	Current(ctx context.Context) ([]LegalDocument, error)
	AcceptedIDs(ctx context.Context, organizationID uuid.UUID) (map[uuid.UUID]bool, error)
	// Accept записывает принятие; повторное принятие того же документа ничего не меняет.
	Accept(ctx context.Context, a LegalAcceptance) error
	// LatestVersion — наибольший version вида; 0, если версий нет.
	LatestVersion(ctx context.Context, kind LegalDocumentKind) (int, error)
	Create(ctx context.Context, d *LegalDocument) error
}

// LoginCodeSender отправляет письмо с кодом. Вызывается только из задания (07 §1).
type LoginCodeSender interface {
	SendLoginCode(ctx context.Context, email, code string, expiresIn time.Duration) error
}

// FileStorage — закрытая область объектного хранилища (07 §4).
type FileStorage interface {
	PutPrivate(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// PlatformOrganization — организация-платформа (organization.is_platform): контрагент
// платформы в ОРД (этап «Маркировка», R-5). Реквизиты задаёт конфигурация.
type PlatformOrganization struct {
	ID        uuid.UUID
	LegalType LegalType
	INN       string
	Name      string
}

// PlatformOrganizations — строка платформы. Platform requires an active
// transaction and serializes bootstrap until that transaction commits/rolls back.
type PlatformOrganizations interface {
	// Platform возвращает ErrNotFound, если строки нет.
	Platform(ctx context.Context) (*PlatformOrganization, error)
	// InsertPlatform — ИНН занят — apperr.INNTaken.
	InsertPlatform(ctx context.Context, p PlatformOrganization, now time.Time) error
	RenamePlatform(ctx context.Context, id uuid.UUID, name string, now time.Time) error
}

// OrganizationListener — реквизиты организации для ОРД (наименование, КПП) изменились
// (этап «Маркировка», R-15). Реализация — marking/app; вызывается в транзакции правки.
type OrganizationListener interface {
	OrganizationChanged(ctx context.Context, organizationID uuid.UUID) error
}

// NopOrganizationListener — модуль маркировки не подключён.
type NopOrganizationListener struct{}

func (NopOrganizationListener) OrganizationChanged(context.Context, uuid.UUID) error { return nil }

// AdminOrganizationFilter — поиск организаций администратором (этап «API администратора», R-13).
type AdminOrganizationFilter struct {
	// Query — начало ИНН, часть наименования или точная почта участника; пусто — все.
	Query string
	// ExactINN — ИНН совпадает целиком, а не началом: модератор видит ИНН под маской и не
	// должен восстанавливать его подбором по началу (R-14).
	ExactINN bool
	// Cabinet — открытый кабинет; пусто — любой.
	Cabinet Cabinet
	Offset  int
	Limit   int
}

// AdminOrganizationItem — строка списка организаций.
type AdminOrganizationItem struct {
	ID         uuid.UUID
	Name       string
	INN        string
	LegalType  LegalType
	Status     Status
	Advertiser bool
	Blogger    bool
	CreatedAt  time.Time
}

// AdminOrganizations — организации для администратора; организация-платформа не видна.
type AdminOrganizations interface {
	AdminList(ctx context.Context, f AdminOrganizationFilter) ([]AdminOrganizationItem, int, error)
	// AdminGet возвращает ErrNotFound, если организации нет.
	AdminGet(ctx context.Context, id uuid.UUID) (*Organization, error)
	// Members — участники, владелец первым.
	Members(ctx context.Context, organizationID uuid.UUID) ([]User, error)
}

// SubjectAcceptance — принятие документа субъектом (выгрузка ПДн, R-26).
type SubjectAcceptance struct {
	Kind       LegalDocumentKind
	Version    int
	AcceptedAt time.Time
	IP         *netip.Addr
}

// SubjectSession — сессия субъекта (выгрузка ПДн).
type SubjectSession struct {
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
	IP        *netip.Addr
	UserAgent *string
}

// PersonalData — выгрузка и обезличивание данных субъекта (152-ФЗ, этап «API администратора», R-26).
type PersonalData interface {
	Acceptances(ctx context.Context, userID uuid.UUID) ([]SubjectAcceptance, error)
	SubjectSessions(ctx context.Context, userID uuid.UUID) ([]SubjectSession, error)
	// MemberCount — участников организации.
	MemberCount(ctx context.Context, organizationID uuid.UUID) (int, error)
	// SaveAnonymized сохраняет почту, статус и отметку обезличивания пользователя.
	SaveAnonymized(ctx context.Context, u *User) error
	// ClearSessions завершает сессии и стирает в них IP и браузер; возвращает число сессий.
	ClearSessions(ctx context.Context, userID uuid.UUID, at time.Time) (int64, error)
	// DeleteLoginCodes удаляет коды входа адреса (и IP запроса в них).
	DeleteLoginCodes(ctx context.Context, email string) (int64, error)
	// ClearOrganizationContacts стирает контактное лицо и телефон организации.
	ClearOrganizationContacts(ctx context.Context, organizationID uuid.UUID, at time.Time) error
}
