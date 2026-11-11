package app

import (
	"context"
	"errors"
	"strings"

	"github.com/leha-creator/chanly/internal/channel/domain"
	"github.com/leha-creator/chanly/internal/platform/job"
)

// ConnectChannelByName is intentionally authorized by bot access, as selected
// by the product owner. It does not infer or check a human MAX identity.
func (s *Service) ConnectChannelByName(ctx context.Context, p ConnectionPayload) error {
	c, err := s.liveConnectionJob(ctx, p, "checking")
	if err != nil || c == nil || c.ChannelName == "" {
		return err
	}
	fail := func(reason string) error { return s.connectionFailed(ctx, p, "checking", reason) }
	ref, err := domain.ParseChannelReference(c.ChannelName)
	if err != nil {
		return fail("channel_not_found")
	}
	ids, err := s.Connections.ResolveReference(ctx, ref)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fail("channel_not_found")
	}
	if len(ids) > 1 {
		return fail("channel_name_ambiguous")
	}
	var ch *domain.Channel
	err = s.Tx.InTx(ctx, func(ctx context.Context) error { var e error; ch, e = s.Channels.GetForUpdate(ctx, ids[0]); return e })
	if errors.Is(err, domain.ErrNotFound) {
		return fail("channel_not_found")
	}
	if err != nil {
		return err
	}
	if ch.OrganizationID != nil && *ch.OrganizationID != c.OrganizationID {
		return fail("linked_to_other")
	}
	if ch.Status == domain.StatusDisconnected || ch.Status == domain.StatusBlocked {
		return fail("channel_unavailable")
	}
	info, bot, reason, checkErr := s.checkBotConnectionRights(ctx, ch)
	if checkErr != nil {
		if _, retry := job.RetryAfter(checkErr); retry {
			return checkErr
		}
		s.log.WarnContext(ctx, "named channel MAX check failed", "op", "Channel.ConnectByName", "connection_id", c.ID)
		return fail("max_unavailable")
	}
	if reason != "" {
		return fail(reason)
	}
	// Do not connect a stale title/link after the channel was renamed in MAX.
	if (ref.Kind == "title" && !strings.EqualFold(strings.TrimSpace(info.Title), ref.Value)) ||
		(ref.Kind == "link" && strings.TrimRight(info.Link, "/") != ref.Value) {
		return fail("channel_changed")
	}
	return s.finishConnection(ctx, p, ch, info, bot, "checking", "bot_access")
}
