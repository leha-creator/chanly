package domain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// Cursor — позиция ключевого курсора списка (created_at, id).
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// Invoices — счета на оплату. Реализация — billing/postgres; методы работают в транзакции
// из context. Выборки кабинета ограничены организацией (07 §4).
type Invoices interface {
	NextNumber(ctx context.Context) (int64, error)
	Insert(ctx context.Context, inv Invoice) error
	// Get — счёт организации; чужой или несуществующий — ErrNotFound.
	Get(ctx context.Context, orgID, id uuid.UUID) (Invoice, error)
	// GetForUpdate — счёт под блокировкой без ограничения организацией (администратор).
	GetForUpdate(ctx context.Context, id uuid.UUID) (Invoice, error)
	// ByNumberForUpdate — счёт по номеру под блокировкой; нет — ErrNotFound.
	ByNumberForUpdate(ctx context.Context, number string) (Invoice, error)
	// ByNumbersForUpdate — счета с номерами из списка под блокировкой.
	ByNumbersForUpdate(ctx context.Context, numbers []string) ([]Invoice, error)
	// IssuedByINNAndAmountForUpdate — выставленные счета плательщика с суммой (05 §5 шаг 3).
	IssuedByINNAndAmountForUpdate(ctx context.Context, inn string, amount money.Kopecks) ([]Invoice, error)
	List(ctx context.Context, orgID uuid.UUID, after *Cursor, limit int) ([]Invoice, error)
	// AdminPage — счета всех организаций для администратора со смещением и общим числом.
	AdminPage(ctx context.Context, f AdminInvoiceFilter) ([]Invoice, int, error)
	// ByIDs — счета по идентификаторам (номера в списке поступлений).
	ByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Invoice, error)
	// DueForExpiry — выставленные счета со сроком не позже now, под блокировкой с пропуском занятых.
	DueForExpiry(ctx context.Context, now time.Time, limit int) ([]Invoice, error)
	// Update сохраняет состояние, оплату и причину отмены.
	Update(ctx context.Context, inv Invoice) error
}

// Statements — загруженные выписки.
type Statements interface {
	Insert(ctx context.Context, s BankStatement) error
	// ExistsByHash — файл с этим SHA-256 уже загружен.
	ExistsByHash(ctx context.Context, sha string) (bool, error)
	UpdateCounts(ctx context.Context, s BankStatement) error
	// Latest — последняя выписка по дате конца периода и времени загрузки; нет — ErrNotFound.
	Latest(ctx context.Context) (BankStatement, error)
	// Get — выписка по идентификатору; LatestUploaded — последняя по времени загрузки.
	Get(ctx context.Context, id uuid.UUID) (BankStatement, error)
	LatestUploaded(ctx context.Context) (BankStatement, error)
}

// BankTransactions — поступления из выписок.
type BankTransactions interface {
	// InsertIfNew записывает поступление; external_id уже есть — false (05 §5 шаг 1).
	InsertIfNew(ctx context.Context, t BankTransaction) (bool, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (BankTransaction, error)
	Update(ctx context.Context, t BankTransaction) error
	List(ctx context.Context, status MatchStatus, after *Cursor, limit int) ([]BankTransaction, error)
	// AdminPage — поступления для администратора со смещением и общим числом.
	AdminPage(ctx context.Context, status MatchStatus, offset, limit int) ([]BankTransaction, int, error)
	// UnmatchedTotal — число и сумма несопоставленных поступлений (сверка с банком, 06 §2.4).
	UnmatchedTotal(ctx context.Context) (int, money.Kopecks, error)
}

// ClosingActs — закрывающие документы.
type ClosingActs interface {
	NextNumber(ctx context.Context) (int64, error)
	Insert(ctx context.Context, a ClosingAct) error
	Exists(ctx context.Context, orgID uuid.UUID, p Period) (bool, error)
	List(ctx context.Context, orgID uuid.UUID, after *Cursor, limit int) ([]ClosingAct, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (ClosingAct, error)
}

// Settlement — распределение удержания размещения из журнала (строка отчёта без сведений о канале).
type Settlement struct {
	PlacementID     uuid.UUID
	AdvertiserOrgID uuid.UUID
	Payout          money.Kopecks
	Fee             money.Kopecks
	SettledAt       time.Time
}

// Ledger — журнал проводок. Реализация — модуль ledger (переходник в bootstrap/bridge).
type Ledger interface {
	// Deposit зачисляет поступление на баланс в транзакции вызывающего (S-30).
	Deposit(ctx context.Context, d DepositRequest) error
	// Settlements — распределения удержаний в периоде [from, to) по рекламодателям.
	Settlements(ctx context.Context, from, to time.Time) ([]Settlement, error)
}

// DepositRequest — зачисление поступления: организация, сумма, основания и автор.
type DepositRequest struct {
	BankTransactionID uuid.UUID
	InvoiceID         uuid.UUID
	OrganizationID    uuid.UUID
	Amount            money.Kopecks
	// AdminID — пусто при автоматическом сопоставлении.
	AdminID uuid.UUID
}

// PlacementInfo — сведения о размещении для отчёта агента.
type PlacementInfo struct {
	OrderNumber  int64
	ChannelTitle string
	PublishedAt  *time.Time
	RemovedAt    *time.Time
}

// Placements — сведения о размещениях. Реализация — модуль placement (переходник).
type Placements interface {
	Info(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]PlacementInfo, error)
}

// OrgRequisites — реквизиты организации для счёта и закрывающих документов.
type OrgRequisites struct {
	ID              uuid.UUID
	Name            string
	INN             string
	KPP             string
	Address         string
	BankAccount     string
	BankName        string
	BankBIK         string
	BankCorrAccount string
	IsPlatform      bool
}

// Payer — снимок плательщика для счёта (D-26).
func (r OrgRequisites) Payer() Payer {
	return Payer{Name: r.Name, INN: r.INN, BankAccount: r.BankAccount, BankName: r.BankName,
		BankBIK: r.BankBIK, BankCorrAccount: r.BankCorrAccount}
}

// Organizations — реквизиты организаций. Реализация — модуль account (переходник).
type Organizations interface {
	Requisites(ctx context.Context, orgID uuid.UUID) (OrgRequisites, error)
	// ByINN — организация по ИНН (ручное сопоставление); нет — ErrNotFound.
	ByINN(ctx context.Context, inn string) (OrgRequisites, error)
}

// Supplier — реквизиты платформы-получателя (R-10).
type Supplier struct {
	Name            string
	INN             string
	OGRNIP          string
	Address         string
	BankAccount     string
	BankName        string
	BankBIK         string
	BankCorrAccount string
}

// InvoiceDoc — данные PDF счёта.
type InvoiceDoc struct {
	Number    string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Amount    money.Kopecks
	Supplier  Supplier
	Payer     Payer
	VATNote   string
}

// ReportDoc — данные PDF отчёта агента.
type ReportDoc struct {
	Number    string
	Period    Period
	IssuedAt  time.Time
	Supplier  Supplier
	Principal OrgRequisites
	Lines     []ReportLine
}

// ActDoc — данные PDF акта на вознаграждение.
type ActDoc struct {
	Number    string
	Period    Period
	IssuedAt  time.Time
	Supplier  Supplier
	Customer  OrgRequisites
	FeeAmount money.Kopecks
	VATNote   string
}

// Renderer — PDF документов. Реализация — billing/pdf.
type Renderer interface {
	Invoice(doc InvoiceDoc) ([]byte, error)
	AgentReport(doc ReportDoc) ([]byte, error)
	Act(doc ActDoc) ([]byte, error)
}

// Notice — уведомление организации (R-17).
type Notice struct {
	Template       string
	OrganizationID uuid.UUID
	ObjectType     string
	ObjectID       string
	Data           map[string]any
}

// Notifier — уведомления; реализация — модуль notification (bootstrap/bridge.BillingNotifier),
// в тестах без него — NopNotifier.
type Notifier interface {
	Notify(ctx context.Context, n Notice) error
}

// Шаблоны уведомлений (R-17).
const (
	NoticeBalanceDeposited = "balance.deposited"
	NoticeClosingActsReady = "closing_acts.ready"
)

// ErrInvalidCursor — курсор страницы испорчен.
var ErrInvalidCursor = errors.New("billing: invalid cursor")

type cursorJSON struct {
	T time.Time `json:"t"`
	I uuid.UUID `json:"i"`
}

// Encode — курсор для ответа API.
func (c Cursor) Encode() string {
	raw, _ := json.Marshal(cursorJSON{T: c.At, I: c.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor разбирает курсор; пустая строка — первая страница.
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var c cursorJSON
	if err := json.Unmarshal(raw, &c); err != nil || c.I == uuid.Nil || c.T.IsZero() {
		return nil, ErrInvalidCursor
	}
	return &Cursor{At: c.T, ID: c.I}, nil
}

// AdminInvoiceFilter — отбор счетов администратора (adm/payments); пустые поля — без отбора.
type AdminInvoiceFilter struct {
	Status         InvoiceStatus
	OrganizationID *uuid.UUID
	Number         string
	Offset, Limit  int
}
