package domain

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
)

// PageCursor — ключевой курсор списков «новые сверху» (R-21): (created_at, id) последней строки.
type PageCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// MediaFiles — оригиналы медиатеки. Выборки кабинета ограничены организацией (07 §4).
type MediaFiles interface {
	Insert(ctx context.Context, f MediaFile) error
	// Get — живой файл организации; иначе ErrNotFound.
	Get(ctx context.Context, orgID, id uuid.UUID) (MediaFile, error)
	// GetAny — файл по идентификатору, в том числе удалённый (задания).
	GetAny(ctx context.Context, id uuid.UUID) (MediaFile, error)
	// FindBySHA256 — живой файл организации с тем же содержимым (R-2).
	FindBySHA256(ctx context.Context, orgID uuid.UUID, sha string) (MediaFile, bool, error)
	// List — живые файлы организации, новые сверху, после курсора.
	List(ctx context.Context, orgID uuid.UUID, after *PageCursor, limit int) ([]MediaFile, error)
	// LiveSubset — какие из ids — живые файлы организации.
	LiveSubset(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]MediaFile, error)
	// SoftDelete помечает файл удалённым; ErrNotFound — нет живого файла.
	SoftDelete(ctx context.Context, orgID, id uuid.UUID, now time.Time) error
	// ForPurge — удалённые до before, не очищенные и не входящие ни в одну версию (R-19).
	ForPurge(ctx context.Context, before time.Time, limit int) ([]MediaFile, error)
	MarkPurged(ctx context.Context, id uuid.UUID, now time.Time) error
}

// Derivatives — производные изображений (R-6).
type Derivatives interface {
	// Ensure создаёт производную в pending по тройке (файл, вид, хеш) или возвращает
	// существующую; очищенная запись возвращается в pending. created — запись новая или снова pending.
	Ensure(ctx context.Context, fileID uuid.UUID, kind DerivativeKind, hash string, now time.Time) (Derivative, bool, error)
	Get(ctx context.Context, id uuid.UUID) (Derivative, error)
	Find(ctx context.Context, fileID uuid.UUID, kind DerivativeKind, hash string) (Derivative, bool, error)
	// ListByFiles — производные файлов заданного вида с одним из хешей.
	ListByFiles(ctx context.Context, fileIDs []uuid.UUID, kind DerivativeKind) ([]Derivative, error)
	// MarkReady записывает построенный файл; false — производная уже готова.
	MarkReady(ctx context.Context, d Derivative) (bool, error)
	// MarkPublished записывает копию в публичной области; false — уже опубликована.
	MarkPublished(ctx context.Context, id uuid.UUID, publicKey, publicURL string, now time.Time) (bool, error)
	// ForPurge — неочищенные производные, созданные до before и не входящие ни в одну версию (R-19).
	ForPurge(ctx context.Context, before time.Time, limit int) ([]Derivative, error)
	// MarkPurged — объект удалён из хранилища; запись возвращается в pending.
	MarkPurged(ctx context.Context, id uuid.UUID, now time.Time) error
}

// Campaigns — кампании организации.
type Campaigns interface {
	Insert(ctx context.Context, c Campaign) error
	Get(ctx context.Context, orgID, id uuid.UUID) (Campaign, error)
	GetForUpdate(ctx context.Context, orgID, id uuid.UUID) (Campaign, error)
	List(ctx context.Context, orgID uuid.UUID, status *CampaignStatus, after *PageCursor, limit int) ([]Campaign, error)
	Update(ctx context.Context, c Campaign) error
}

// Posts — посты кампаний.
type Posts interface {
	// LockDraftMedia serializes draft selection and media deletion in one organization.
	// Acquire before post/media row locks, inside the caller's transaction.
	LockDraftMedia(ctx context.Context, orgID uuid.UUID) error
	Insert(ctx context.Context, p Post) error
	GetByCampaign(ctx context.Context, orgID, campaignID uuid.UUID) (Post, error)
	GetByCampaignForUpdate(ctx context.Context, orgID, campaignID uuid.UUID) (Post, error)
	SaveDraft(ctx context.Context, p Post) error
	// RemoveMediaFromDrafts returns updated drafts, preserving the remaining order.
	RemoveMediaFromDrafts(ctx context.Context, orgID, fileID uuid.UUID, now time.Time) ([]Post, error)
}

// Versions — версии поста.
type Versions interface {
	// NextNumber — следующий номер версии поста; вызывается под блокировкой поста.
	NextNumber(ctx context.Context, postID uuid.UUID) (int, error)
	Insert(ctx context.Context, v Version) error
	Get(ctx context.Context, id uuid.UUID) (Version, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (Version, error)
	ListByPost(ctx context.Context, postID uuid.UUID) ([]Version, error)
	// UpdateStatus сохраняет переход версии.
	UpdateStatus(ctx context.Context, v Version) error
	// Organization — организация-рекламодатель версии (этап «Маркировка»).
	Organization(ctx context.Context, versionID uuid.UUID) (uuid.UUID, error)
}

// VersionChange — смена состояния версии поста (03 §1.1).
type VersionChange struct {
	VersionID uuid.UUID
	PostID    uuid.UUID
	From      VersionStatus
	To        VersionStatus
}

// VersionListener — последствия смены состояния версии для заказа и размещений (R-5 этапа
// «Заказ и размещения»). Вызывается в транзакции перехода версии: ошибка откатывает переход.
// Реализация — модуль размещений; без него — NopVersionListener.
type VersionListener interface {
	VersionChanged(ctx context.Context, change VersionChange) error
}

// AdvertiserRequisites — название и ИНН текущей организации (R-14). Реализация — переходник в bootstrap.
type AdvertiserRequisites interface {
	Requisites(ctx context.Context, orgID uuid.UUID) (Requisites, error)
}

// Rendered — построенная производная.
type Rendered struct {
	Data          []byte
	Mime          string
	Width, Height int
}

// Renderer строит производные и читает сведения о файле. Реализация — post/imaging.
type Renderer interface {
	// Probe читает сведения о файле без полного декодирования (R-3).
	Probe(data []byte) UploadInfo
	// Render строит производную из оригинала. Ошибка декодирования — ErrUndecodable,
	// производная больше предела — ErrDerivativeTooLarge.
	Render(ctx context.Context, original []byte, p DerivativeParams) (Rendered, error)
}

// ObjectStore — нужное модулю подмножество platform/storage.
type ObjectStore interface {
	PutPrivate(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, key string) error
	CopyToPublic(ctx context.Context, privateKey, publicKey, contentType string) error
	PublicURL(publicKey string) string
	NewPublicKey(ext string) string
}
