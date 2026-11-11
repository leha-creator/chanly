package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
)

const KindScanChannelConnection = "scan_channel_connection"
const KindClaimChannelConnection = "claim_channel_connection"
const connectionBatch = 20
const connectionRefreshDelay = 10 * time.Second

type ConnectionPayload struct {
	ConnectionID uuid.UUID `json:"connection_id"`
	ScanID       uuid.UUID `json:"scan_id"`
	After        uuid.UUID `json:"after"`
}

type ConnectionView struct {
	Connection *domain.Connection
	Candidates []domain.ConnectionCandidate
	BotURL     string // returned only at creation; never stored or logged
}

func (s *Service) CreateConnection(ctx context.Context, orgID, userID uuid.UUID) (*ConnectionView, error) {
	bot := strings.TrimPrefix(s.Config.BotName, "@")
	if bot == "" {
		return nil, apperr.New(apperr.CodeServiceUnavailable, "MAX bot name is not configured")
	}
	now := s.Clock.Now()
	token := domain.NewConnectionToken()
	c := &domain.Connection{ID: ids.New(), OrganizationID: orgID, UserID: userID,
		TokenHash: domain.HashConnectionToken(s.Config.Pepper, token), ExpiresAt: now.Add(s.Config.LinkCodeTTL),
		Status: "pending", ScanID: ids.New(), CreatedAt: now, UpdatedAt: now}
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		n, err := s.Connections.CountSince(ctx, orgID, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= s.Config.LinkCodesPerHour {
			return apperr.RateLimited("channel_connection", time.Hour)
		}
		return s.Connections.Create(ctx, c)
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "channel connection created", "op", "Channel.Connection.Create", "connection_id", c.ID)
	return &ConnectionView{Connection: c, Candidates: []domain.ConnectionCandidate{}, BotURL: "https://max.ru/" + url.PathEscape(bot) + "?start=connect_" + token}, nil
}

func (s *Service) ownConnection(ctx context.Context, org, user, id uuid.UUID, lock bool) (*domain.Connection, error) {
	c, err := s.Connections.Get(ctx, id, lock)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("connection not found")
	}
	if err != nil {
		return nil, err
	}
	if c.OrganizationID != org || c.UserID != user {
		return nil, apperr.NotFound("connection not found")
	}
	return c, nil
}

func (s *Service) GetConnection(ctx context.Context, org, user, id uuid.UUID) (*ConnectionView, error) {
	// Consistent status and candidates while a worker finishes or a refresh starts.
	var v *ConnectionView
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.ownConnection(ctx, org, user, id, true)
		if err != nil {
			return err
		}
		v = &ConnectionView{Connection: c, Candidates: []domain.ConnectionCandidate{}}
		if !s.Clock.Now().Before(c.ExpiresAt) {
			c.Status = "expired"
			c.Reason = "expired"
			return nil
		}
		if c.Status == "ready" {
			v.Candidates, err = s.Connections.Candidates(ctx, id)
		}
		return err
	})
	return v, err
}

func (s *Service) saveConnection(ctx context.Context, c *domain.Connection) error {
	c.UpdatedAt = s.Clock.Now()
	s.log.InfoContext(ctx, "channel connection state", "op", "Channel.Connection.Save", "connection_id", c.ID, "status", c.Status, "reason", c.Reason)
	return s.Connections.Save(ctx, c)
}

func (s *Service) enqueueConnection(ctx context.Context, c *domain.Connection, kind string, after uuid.UUID) error {
	return s.Jobs.Enqueue(ctx, job.Job{Kind: kind,
		DedupeKey: kind + ":" + c.ID.String() + ":" + c.ScanID.String() + ":" + after.String(),
		Payload:   ConnectionPayload{ConnectionID: c.ID, ScanID: c.ScanID, After: after}, MaxAttempts: 5})
}

// StartConnection consumes the link once, from a signed/validated MAX webhook.
// Replay cannot replace the MAX identity, even for the same connection.
func (s *Service) StartConnection(ctx context.Context, userID int64, payload string) error {
	if userID <= 0 || !strings.HasPrefix(payload, "connect_") {
		return ErrIgnored
	}
	token := strings.TrimPrefix(payload, "connect_")
	if len(token) != 43 {
		return ErrIgnored
	}
	return s.StartConnectionHash(ctx, userID, domain.HashConnectionToken(s.Config.Pepper, token))
}

// StartConnectionHash accepts only the server-computed hash persisted by the
// authenticated webhook adapter, keeping bearer links out of inbound_event.
func (s *Service) StartConnectionHash(ctx context.Context, userID int64, tokenHash string) error {
	if userID <= 0 || len(tokenHash) != 64 {
		return ErrIgnored
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Connections.FindTokenForUpdate(ctx, tokenHash)
		if errors.Is(err, domain.ErrNotFound) {
			return ErrIgnored
		}
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		if c.TokenUsedAt != nil || c.Status != "pending" || !now.Before(c.ExpiresAt) {
			return ErrIgnored
		}
		c.MaxUserID = &userID
		c.TokenUsedAt = &now
		c.Status = "checking"
		if err = s.saveConnection(ctx, c); err != nil {
			return err
		}
		return s.enqueueConnection(ctx, c, KindScanChannelConnection, uuid.Nil)
	})
}

func (s *Service) RefreshConnection(ctx context.Context, org, user, id uuid.UUID) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.ownConnection(ctx, org, user, id, true)
		if err != nil {
			return err
		}
		if !s.Clock.Now().Before(c.ExpiresAt) {
			return apperr.Conflict("connection expired")
		}
		if c.MaxUserID == nil {
			return apperr.Conflict("open the bot link first")
		}
		if c.Status == "checking" {
			return nil
		} // polling/repeated clicks do not multiply jobs
		if c.Status == "connecting" || c.Status == "connected" {
			return apperr.Conflict("connection already submitted")
		}
		if s.Clock.Now().Sub(c.UpdatedAt) < connectionRefreshDelay {
			return apperr.RateLimited("channel_connection_refresh", connectionRefreshDelay)
		}
		c.Status = "checking"
		c.Reason = ""
		c.ChannelID = nil
		c.ScanID = ids.New()
		if err = s.Connections.ClearCandidates(ctx, id); err != nil {
			return err
		}
		if err = s.saveConnection(ctx, c); err != nil {
			return err
		}
		return s.enqueueConnection(ctx, c, KindScanChannelConnection, uuid.Nil)
	})
}

func (s *Service) ClaimConnection(ctx context.Context, org, user, id, chID uuid.UUID) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.ownConnection(ctx, org, user, id, true)
		if err != nil {
			return err
		}
		if !s.Clock.Now().Before(c.ExpiresAt) {
			return apperr.Conflict("connection expired")
		}
		if (c.Status == "connecting" || c.Status == "connected") && c.ChannelID != nil && *c.ChannelID == chID {
			return nil
		}
		if c.Status != "ready" || c.MaxUserID == nil {
			return apperr.Conflict("connection is not ready")
		}
		ok, err := s.Connections.HasCandidate(ctx, id, chID)
		if err != nil {
			return err
		}
		if !ok {
			return apperr.NotFound("channel candidate not found")
		}
		c.ChannelID = &chID
		c.Status = "connecting"
		c.Reason = ""
		if err = s.saveConnection(ctx, c); err != nil {
			return err
		}
		return s.enqueueConnection(ctx, c, KindClaimChannelConnection, uuid.Nil)
	})
}

func (s *Service) liveConnectionJob(ctx context.Context, p ConnectionPayload, status string) (*domain.Connection, error) {
	c, err := s.Connections.Get(ctx, p.ConnectionID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.ScanID != p.ScanID || c.Status != status || c.MaxUserID == nil || !s.Clock.Now().Before(c.ExpiresAt) {
		return nil, nil
	}
	return c, nil
}

func (s *Service) connectionFailed(ctx context.Context, p ConnectionPayload, status, reason string) error {
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Connections.Get(ctx, p.ConnectionID, true)
		if err != nil {
			return err
		}
		if c.ScanID != p.ScanID || c.Status != status {
			return nil
		}
		c.Status = "failed"
		c.Reason = reason
		return s.saveConnection(ctx, c)
	})
}

// checkConnectionRights performs all remote calls outside transactions.
func (s *Service) checkConnectionRights(ctx context.Context, c *domain.Channel, userID int64) (domain.ChatInfo, domain.Membership, string, error) {
	user, err := s.UserMemberships.GetUserMembership(ctx, c.MaxChatID, userID)
	if errors.Is(err, domain.ErrChannelAccessDenied) {
		return domain.ChatInfo{}, domain.Membership{}, "channel_unavailable", nil
	}
	if err != nil {
		return domain.ChatInfo{}, domain.Membership{}, "", err
	}
	if !user.CanConnect() {
		return domain.ChatInfo{}, domain.Membership{}, "user_rights_required", nil
	}
	bot, err := s.Chats.GetMembership(ctx, c.MaxChatID)
	if errors.Is(err, domain.ErrChannelAccessDenied) {
		return domain.ChatInfo{}, bot, "channel_unavailable", nil
	}
	if err != nil {
		return domain.ChatInfo{}, bot, "", err
	}
	probe := domain.Channel{BotIsAdmin: bot.IsAdmin, BotPermissions: bot.Permissions}
	if len(probe.MissingPermissions()) > 0 {
		return domain.ChatInfo{}, bot, "bot_rights_required", nil
	}
	info, err := s.Chats.GetChat(ctx, c.MaxChatID)
	if errors.Is(err, domain.ErrChannelAccessDenied) {
		return info, bot, "channel_unavailable", nil
	}
	if err != nil {
		return info, bot, "", err
	}
	if !info.IsChannel {
		return info, bot, "not_channel", nil
	}
	if !info.IsPublic && s.Config.Visibility != domain.AllowPrivateChannels {
		return info, bot, "not_public", nil
	}
	return info, bot, "", nil
}

func (s *Service) ScanConnection(ctx context.Context, p ConnectionPayload) error {
	c, err := s.liveConnectionJob(ctx, p, "checking")
	if err != nil || c == nil {
		return err
	}
	batch, err := s.Connections.ListDiscovered(ctx, c.OrganizationID, p.After, connectionBatch)
	if err != nil {
		return err
	}
	eligible := []uuid.UUID{}
	for _, id := range batch {
		// A short read transaction obtains the snapshot; no MAX calls hold locks.
		var ch *domain.Channel
		err = s.Tx.InTx(ctx, func(ctx context.Context) error { var e error; ch, e = s.Channels.GetForUpdate(ctx, id); return e })
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		_, _, reason, checkErr := s.checkConnectionRights(ctx, ch, *c.MaxUserID)
		if checkErr != nil {
			// Never turn a network/API failure into a verified empty result.
			if _, postponed := job.RetryAfter(checkErr); postponed {
				return checkErr
			}
			s.log.WarnContext(ctx, "channel connection MAX check failed", "op", "Channel.Connection.Scan", "connection_id", c.ID)
			return s.connectionFailed(ctx, p, "checking", "max_unavailable")
		}
		if reason == "" {
			eligible = append(eligible, id)
		}
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		current, err := s.Connections.Get(ctx, c.ID, true)
		if err != nil {
			return err
		}
		if current.ScanID != p.ScanID || current.Status != "checking" || !s.Clock.Now().Before(current.ExpiresAt) {
			return nil
		}
		for _, id := range eligible {
			if err = s.Connections.AddCandidate(ctx, c.ID, id); err != nil {
				return err
			}
		}
		s.log.DebugContext(ctx, "channel candidates checked", "op", "Channel.Connection.Scan", "connection_id", c.ID, "count", len(eligible))
		if len(batch) == connectionBatch {
			return s.enqueueConnection(ctx, current, KindScanChannelConnection, batch[len(batch)-1])
		}
		current.Status = "ready"
		return s.saveConnection(ctx, current)
	})
}

func (s *Service) CompleteConnection(ctx context.Context, p ConnectionPayload) error {
	c, err := s.liveConnectionJob(ctx, p, "connecting")
	if err != nil || c == nil {
		return err
	}
	if c.ChannelID == nil {
		return s.connectionFailed(ctx, p, "connecting", "channel_unavailable")
	}
	var ch *domain.Channel
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		var e error
		ch, e = s.Channels.GetForUpdate(ctx, *c.ChannelID)
		return e
	})
	if errors.Is(err, domain.ErrNotFound) {
		return s.connectionFailed(ctx, p, "connecting", "channel_unavailable")
	}
	if err != nil {
		return err
	}
	info, bot, reason, checkErr := s.checkConnectionRights(ctx, ch, *c.MaxUserID)
	if checkErr != nil {
		if _, retry := job.RetryAfter(checkErr); retry {
			return checkErr
		}
		return s.connectionFailed(ctx, p, "connecting", "max_unavailable")
	}
	if reason != "" {
		return s.connectionFailed(ctx, p, "connecting", reason)
	}
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		current, err := s.Connections.Get(ctx, c.ID, true)
		if err != nil {
			return err
		}
		if current.ScanID != p.ScanID || current.Status != "connecting" || !s.Clock.Now().Before(current.ExpiresAt) {
			return nil
		}
		locked, err := s.Channels.GetForUpdate(ctx, ch.ID)
		if err != nil {
			return err
		}
		fail := func(reason string) error {
			current.Status = "failed"
			current.Reason = reason
			return s.saveConnection(ctx, current)
		}
		if locked.OrganizationID != nil && *locked.OrganizationID != current.OrganizationID {
			return fail("linked_to_other")
		}
		if locked.Status == domain.StatusDisconnected || locked.Status == domain.StatusBlocked {
			return fail("channel_unavailable")
		}
		if locked.LockVersion != ch.LockVersion {
			return fail("channel_changed")
		}
		change := locked.RightsRefreshed(info, bot, s.Config.RknThreshold, s.Clock.Now(), s.Config.Visibility)
		if err = s.save(ctx, "Channel.Connection.Refresh", locked, change); err != nil {
			return err
		}
		change, err = locked.LinkTo(current.OrganizationID, s.Config.RknThreshold, s.Clock.Now(), s.Config.Visibility)
		if err != nil && !errors.Is(err, domain.ErrAlreadyLinked) {
			return fail("channel_unavailable")
		}
		if err == nil {
			if err = s.save(ctx, "Channel.Connection.Claim", locked, change); err != nil {
				return err
			}
		}
		actorCtx := audit.WithActor(ctx, audit.Actor{Type: audit.ActorBlogger, UserID: &current.UserID, OrganizationID: &current.OrganizationID})
		if err = s.Audit.Record(actorCtx, audit.Entry{Action: "channel.linked", ObjectType: "channel", ObjectID: locked.ID.String(),
			ActorType: audit.ActorBlogger, OrganizationID: &current.OrganizationID,
			Data: map[string]any{"max_chat_id": locked.MaxChatID, "method": "bot_dialog"}}); err != nil {
			return err
		}
		current.Status = "connected"
		current.Reason = ""
		return s.saveConnection(ctx, current)
	})
}
