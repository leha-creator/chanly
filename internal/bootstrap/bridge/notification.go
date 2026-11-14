package bridge

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	billingdomain "github.com/leha-creator/chanly/internal/billing/domain"
	channeldomain "github.com/leha-creator/chanly/internal/channel/domain"
	channelpg "github.com/leha-creator/chanly/internal/channel/postgres"
	"github.com/leha-creator/chanly/internal/integration/max"
	notificationapp "github.com/leha-creator/chanly/internal/notification/app"
	notificationdomain "github.com/leha-creator/chanly/internal/notification/domain"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	placementdomain "github.com/leha-creator/chanly/internal/placement/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

// ---------------------------------------------------------------- notification ← account, channel, MAX

// NotificationRecipients — notification/domain.Recipients поверх account/postgres (R-1).
type NotificationRecipients struct{ Users *accountpg.Users }

var _ notificationdomain.Recipients = NotificationRecipients{}
var _ notificationdomain.MAXRecipients = NotificationRecipients{}

func (b NotificationRecipients) Members(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	return b.Users.MemberIDs(ctx, orgID)
}

func (b NotificationRecipients) Admins(ctx context.Context) ([]uuid.UUID, error) {
	return b.Users.AdminIDs(ctx)
}

func (b NotificationRecipients) Email(ctx context.Context, userID uuid.UUID) (string, error) {
	return b.Users.ActiveEmail(ctx, userID)
}

func (b NotificationRecipients) NotificationMAXRecipient(ctx context.Context, userID uuid.UUID) (int64, error) {
	return b.Users.NotificationMAXRecipient(ctx, userID)
}

// NotificationChannels — notification/domain.Channels поверх channel/postgres (R-3, R-6).
type NotificationChannels struct{ Channels *channelpg.Channels }

var _ notificationdomain.Channels = NotificationChannels{}

func (b NotificationChannels) Channel(ctx context.Context, id uuid.UUID) (notificationdomain.ChannelInfo, error) {
	list, err := b.Channels.ListByIDs(ctx, []uuid.UUID{id})
	if err != nil {
		return notificationdomain.ChannelInfo{}, err
	}
	if len(list) == 0 {
		return notificationdomain.ChannelInfo{}, notificationdomain.ErrNotFound
	}
	return notificationdomain.ChannelInfo{Title: list[0].Title, AddedByMaxUserID: list[0].AddedByMaxUserID}, nil
}

// MAXUserMessenger — личные сообщения бота (integration/max или maxfake).
type MAXUserMessenger interface {
	SendUserMessage(ctx context.Context, userID int64, text string) (string, error)
}

// NotificationMessenger — notification/domain.Messenger поверх MAX: приоритет ограничителя
// «ответы и уведомления» (05 §2.8). Отказ 4xx уже помечен outbound как job.Permanent.
// ПРЕДПОЛОЖЕНИЕ: бот не может писать пользователю без начатого диалога — MAX отвечает 403
// `chat.denied` или 404 `user.not.found` (Q-38, E-1); это отказ без повторов.
type NotificationMessenger struct {
	MAX MAXUserMessenger
	Log *slog.Logger
}

var _ notificationdomain.Messenger = NotificationMessenger{}

func (b NotificationMessenger) SendUserMessage(ctx context.Context, maxUserID int64, text string) error {
	ctx = max.WithPriority(ctx, ratelimit.PriorityReply)
	_, err := b.MAX.SendUserMessage(ctx, maxUserID, text)
	if err == nil {
		return nil
	}
	if e, ok := max.AsAPIError(err); ok {
		b.Log.DebugContext(ctx, "max user message rejected", logx.KeyOp, "Notification.MAX", "max_user_id", maxUserID, "code", e.Code)
	}
	return err
}

// ---------------------------------------------------------------- notification ← placement, billing

// notifier — модуль уведомлений для переходников событий.
type notifier interface {
	Notify(ctx context.Context, n notificationdomain.Notice) error
}

var _ notifier = (*notificationapp.Service)(nil)

// PlacementNotifier — placement/domain.Notifier поверх модуля уведомлений (R-4 этапа заказов).
type PlacementNotifier struct{ Svc notifier }

var _ placementdomain.Notifier = PlacementNotifier{}

func (b PlacementNotifier) Notify(ctx context.Context, n placementdomain.Notice) error {
	role := notificationdomain.RoleAdvertiser
	switch n.Role {
	case placementdomain.ActorBlogger:
		role = notificationdomain.RoleBlogger
	case placementdomain.ActorAdmin:
		role = notificationdomain.RoleAdmin
	}
	return b.Svc.Notify(ctx, notificationdomain.Notice{Template: n.Template, OrganizationID: n.OrganizationID, Role: role,
		ObjectType: n.ObjectType, ObjectID: n.ObjectID, Data: n.Data, Key: n.Key})
}

// BillingNotifier — billing/domain.Notifier поверх модуля уведомлений: рекламодателю (R-17).
type BillingNotifier struct{ Svc notifier }

var _ billingdomain.Notifier = BillingNotifier{}

func (b BillingNotifier) Notify(ctx context.Context, n billingdomain.Notice) error {
	if n.OrganizationID == uuid.Nil {
		return errors.New("billing notice without organization")
	}
	return b.Svc.Notify(ctx, notificationdomain.Notice{Template: n.Template, OrganizationID: n.OrganizationID,
		Role: notificationdomain.RoleAdvertiser, ObjectType: n.ObjectType, ObjectID: n.ObjectID, Data: n.Data})
}

// ChannelNotifier — channel/domain.Notifier поверх модуля уведомлений: блогеру о канале.
type ChannelNotifier struct{ Svc notifier }

var _ channeldomain.Notifier = ChannelNotifier{}

func (b ChannelNotifier) Notify(ctx context.Context, n channeldomain.Notice) error {
	return b.Svc.Notify(ctx, notificationdomain.Notice{Template: n.Template, OrganizationID: n.OrganizationID,
		Role: notificationdomain.RoleBlogger, ObjectType: "channel", ObjectID: n.ChannelID.String(), Data: n.Data})
}

// PayoutNotifier — payout/domain.Notifier поверх модуля уведомлений: блогеру о выплате
// (объект payout, повтор по коду и выплате не дублирует) и о статусе исполнителя Рокет Ворк
// (объект rocketwork_contractor, без ключа: статус может меняться туда и обратно). Ссылка —
// /blg/payouts (notification/domain.Link).
type PayoutNotifier struct{ Svc notifier }

var _ payoutdomain.Notifier = PayoutNotifier{}

func (b PayoutNotifier) Notify(ctx context.Context, n payoutdomain.Notice) error {
	if n.OrganizationID == uuid.Nil {
		return errors.New("payout notice without organization")
	}
	out := notificationdomain.Notice{Template: n.Template, OrganizationID: n.OrganizationID,
		Role: notificationdomain.RoleBlogger, ObjectType: "payout", ObjectID: n.ObjectID.String(), Data: n.Data}
	if strings.HasPrefix(n.Template, "rocketwork.") {
		out.ObjectType = "rocketwork_contractor"
	} else {
		out.Key = n.Template + ":" + n.ObjectID.String()
	}
	return b.Svc.Notify(ctx, out)
}
