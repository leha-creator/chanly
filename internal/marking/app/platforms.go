package app

import (
	"context"
	"errors"
	"time"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ChannelApproved — канал одобрен модератором (S-16 шаг 3, R-13; порт
// channel/domain.ChannelApprovalListener через переходник). В транзакции одобрения: площадка
// (новая или с изменённой нагрузкой) — pending и задание; контрагент блогера и договор с
// блогером — зависимости. Площадка уже готова с той же нагрузкой — канал сразу виден (R-14).
// ОРД здесь не вызывается: его ошибка одобрение не отменяет.
func (s *Service) ChannelApproved(ctx context.Context, ch domain.ChannelRef) error {
	const op = "Marking.ChannelApproved"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.EnsurePublisherChain(ctx, ch.OrganizationID); err != nil {
			return err
		}
		now := s.Clock.Now()
		name := domain.PlatformName(ch.Title)
		p, err := s.Platforms.ByChannel(ctx, ch.ID)
		if errors.Is(err, domain.ErrNotFound) {
			rec, created, err := s.Platforms.Insert(ctx, domain.Platform{
				ID: ids.New(), ChannelID: ch.ID, OrganizationID: ch.OrganizationID, Name: name, URL: ch.Link,
				Sync: domain.NewSyncState(), CreatedAt: now, UpdatedAt: now,
			})
			if err != nil || !created {
				return err
			}
			s.log.InfoContext(ctx, "ord platform queued", logx.KeyOp, op, KeyChannelID, ch.ID, "reason", reasonNew)
			return s.enqueueSync(ctx, op, rec.Ref(), reasonNew)
		}
		if err != nil {
			return err
		}
		if p, err = s.Platforms.GetForUpdate(ctx, p.ID); err != nil {
			return err
		}
		p.Name, p.URL, p.OrganizationID = name, ch.Link, ch.OrganizationID
		hash, err := domain.PayloadHash(domain.NewPlatformRequest(p, s.Config.PlatformType))
		if err != nil {
			return err
		}
		if p.Sync.Ready() && !p.Sync.NeedsResend(hash) {
			s.log.DebugContext(ctx, "ord platform up to date", logx.KeyOp, op, KeyChannelID, ch.ID)
			return s.Channels.SetOrdPlatformReady(ctx, ch.ID, now)
		}
		if !p.Sync.Requeue(hash) {
			s.log.DebugContext(ctx, "ord platform already queued", logx.KeyOp, op, KeyChannelID, ch.ID, "sync_status", p.Sync.Status)
			return nil
		}
		p.UpdatedAt = now
		if err := s.Platforms.Save(ctx, p); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "ord platform queued", logx.KeyOp, op, KeyChannelID, ch.ID, "reason", reasonChanged)
		return s.enqueueSync(ctx, op, p.Ref(), reasonChanged)
	})
}

// buildPlatform — запрос площадки (R-13): зависит только от контрагента блогера (R-4).
func (s *Service) buildPlatform(ctx context.Context, r *platformRecord) (*outgoing, error, error) {
	const op = "Marking.Sync"
	blogger, err := s.ensureOrganization(ctx, r.v.OrganizationID, true)
	if err != nil {
		return nil, nil, err
	}
	v := domain.DependencyVerdict([]domain.Dependency{dependency(domain.NeedBloggerOrganization, blogger.Ref(), blogger.Sync)})
	if !v.Ready() {
		return nil, s.wait(ctx, op, r.ref(), v), nil
	}
	req := domain.NewPlatformRequest(r.v, s.Config.PlatformType)
	hash, err := domain.PayloadHash(req)
	if err != nil {
		return nil, nil, err
	}
	return &outgoing{
		ref: r.ref(), externalID: r.externalID(), hash: hash,
		call: func(ctx context.Context, api domain.OrdAPI) (domain.Receipt, error) {
			return api.UpsertPlatform(ctx, req)
		},
	}, nil, nil
}

// platformStatus — площадка готова (R-2: принята ОРД или подтверждена): канал виден в каталоге (R-14).
func (s *Service) platformStatus(ctx context.Context, r *platformRecord, now time.Time) error {
	if !r.v.Sync.Ready() {
		return nil
	}
	if err := s.Channels.SetOrdPlatformReady(ctx, r.v.ChannelID, now); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "channel visible after ord platform", logx.KeyOp, "Marking.PlatformReady", KeyChannelID, r.v.ChannelID,
		"wait_ms", now.Sub(r.v.CreatedAt).Milliseconds())
	return nil
}
