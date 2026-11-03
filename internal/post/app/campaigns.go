package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/post/domain"
)

// CampaignPage — страница кампаний; NextCursor пуст на последней странице.
type CampaignPage struct {
	Items      []domain.Campaign
	NextCursor string
}

// CreateCampaign создаёт кампанию в draft и пустой пост к ней в одной транзакции (D-16, R-1).
func (s *Service) CreateCampaign(ctx context.Context, orgID, userID uuid.UUID, in domain.CampaignInput) (domain.Campaign, error) {
	const op = "Post.CreateCampaign"
	now := s.Clock.Now()
	c, err := domain.NewCampaign(ids.New(), orgID, userID, in, now)
	if err != nil {
		s.logRuleRejected(ctx, op, "campaign create rejected", uuid.Nil, err)
		return domain.Campaign{}, ruleError(err)
	}
	p := domain.NewPost(ids.New(), orgID, c.ID, now)
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.Campaigns.Insert(ctx, c); err != nil {
			return err
		}
		if err := s.Posts.Insert(ctx, p); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionCampaignCreated, ObjectType: "campaign", ObjectID: c.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"post_id": p.ID},
		})
	})
	if err != nil {
		return domain.Campaign{}, err
	}
	s.log.InfoContext(ctx, "campaign created", logx.KeyOp, op, KeyCampaignID, c.ID, KeyPostID, p.ID)
	return c, nil
}

// ListCampaigns — кампании организации, новые сверху.
func (s *Service) ListCampaigns(ctx context.Context, orgID uuid.UUID, status *domain.CampaignStatus, cursorStr string, limit int) (CampaignPage, error) {
	after, err := cursor(cursorStr)
	if err != nil {
		return CampaignPage{}, err
	}
	limit = pageLimit(limit)
	list, err := s.Campaigns.List(ctx, orgID, status, after, limit+1)
	if err != nil {
		return CampaignPage{}, err
	}
	page := CampaignPage{Items: list}
	if len(list) > limit {
		page.Items = list[:limit]
		last := list[limit-1]
		page.NextCursor = domain.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	return page, nil
}

// GetCampaign — кампания организации.
func (s *Service) GetCampaign(ctx context.Context, orgID, id uuid.UUID) (domain.Campaign, error) {
	c, err := s.Campaigns.Get(ctx, orgID, id)
	return c, notFound(err, "campaign")
}

// UpdateCampaign меняет переданные поля (R-17): период и требования — только в draft (409).
func (s *Service) UpdateCampaign(ctx context.Context, orgID, userID, id uuid.UUID, in domain.CampaignInput) (domain.Campaign, error) {
	const op = "Post.UpdateCampaign"
	var out domain.Campaign
	var changed []string
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Campaigns.GetForUpdate(ctx, orgID, id)
		if err != nil {
			return notFound(err, "campaign")
		}
		before := campaignAudit(c)
		if changed, err = c.Update(in, s.Clock.Now()); err != nil {
			if errors.Is(err, domain.ErrNotDraft) {
				s.log.WarnContext(ctx, "campaign update rejected", logx.KeyOp, op, KeyCampaignID, id, "reason", reasonNotDraft)
				return apperr.Conflict("campaign period can be changed only in draft").
					WithDetail("reason", reasonNotDraft).WithDetail("status", string(c.Status))
			}
			s.logRuleRejected(ctx, op, "campaign update rejected", id, err)
			return ruleError(err)
		}
		out = c
		if len(changed) == 0 {
			return nil
		}
		if err := s.Campaigns.Update(ctx, c); err != nil {
			return err
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: actionCampaignUpdated, ObjectType: "campaign", ObjectID: id.String(), ActorType: audit.ActorAdvertiser,
			Data: audit.Diff(before, campaignAudit(c), changed),
		})
	})
	if err != nil {
		return domain.Campaign{}, err
	}
	s.log.InfoContext(ctx, "campaign updated", logx.KeyOp, op, KeyCampaignID, id, "fields", changed)
	return out, nil
}

// campaignAudit — значения кампании для журнала действий. Текст требований не пишется:
// только признак, что он задан.
func campaignAudit(c domain.Campaign) map[string]any {
	m := map[string]any{"name": c.Name, "extra_requirements": c.ExtraRequirements != nil}
	if c.DateFrom != nil {
		m["date_from"] = c.DateFrom.Format("2006-01-02")
	}
	if c.DateTo != nil {
		m["date_to"] = c.DateTo.Format("2006-01-02")
	}
	if c.TimeFrom != nil {
		m["time_from"] = c.TimeFrom.String()
	}
	if c.TimeTo != nil {
		m["time_to"] = c.TimeTo.String()
	}
	return m
}

// logRuleRejected пишет предметный отказ: поле и причину, без значений.
func (s *Service) logRuleRejected(ctx context.Context, op, msg string, campaignID uuid.UUID, err error) {
	var re *domain.RuleError
	if !errors.As(err, &re) {
		return
	}
	args := []any{logx.KeyOp, op, "field", re.Field, "reason", re.Reason}
	if campaignID != uuid.Nil {
		args = append(args, KeyCampaignID, campaignID)
	}
	s.log.WarnContext(ctx, msg, args...)
}

// CheckoutCampaign — кампания под блокировкой для оформления заказа и её пост.
type CheckoutCampaign struct {
	Campaign domain.Campaign
	PostID   uuid.UUID
}

// LockCampaignForCheckout — кампания организации с блокировкой строки до конца транзакции
// вызывающего (R-1 этапа «Заказ и размещения»): два одновременных оформления не создают
// двух заказов. Чужая — 404.
func (s *Service) LockCampaignForCheckout(ctx context.Context, orgID, campaignID uuid.UUID) (CheckoutCampaign, error) {
	c, err := s.Campaigns.GetForUpdate(ctx, orgID, campaignID)
	if err != nil {
		return CheckoutCampaign{}, notFound(err, "campaign")
	}
	p, err := s.Posts.GetByCampaign(ctx, orgID, campaignID)
	if err != nil {
		return CheckoutCampaign{}, notFound(err, "campaign")
	}
	return CheckoutCampaign{Campaign: c, PostID: p.ID}, nil
}

// MarkCampaignOrdered — кампания оплачена (draft → ordered) со ссылкой на заказ (R-1, R-19).
// Работает в транзакции вызывающего. Не draft — 409.
func (s *Service) MarkCampaignOrdered(ctx context.Context, orgID, campaignID, orderID uuid.UUID) error {
	const op = "Post.MarkCampaignOrdered"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Campaigns.GetForUpdate(ctx, orgID, campaignID)
		if err != nil {
			return notFound(err, "campaign")
		}
		from := c.Status
		if err := c.MarkOrdered(orderID, s.Clock.Now()); err != nil {
			s.log.WarnContext(ctx, "campaign status change rejected", logx.KeyOp, op, KeyCampaignID, campaignID,
				"reason", reasonNotDraft, "status", from)
			return apperr.Conflict("campaign is not a draft").WithDetail("reason", reasonNotDraft).
				WithDetail("status", string(from))
		}
		if err := s.Campaigns.Update(ctx, c); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionCampaignOrdered, ObjectType: "campaign", ObjectID: c.ID.String(), ActorType: audit.ActorAdvertiser,
			Data: map[string]any{"from": from, "to": c.Status, "order_id": orderID},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "campaign status changed", logx.KeyOp, op, KeyCampaignID, c.ID, "from", from, "to", c.Status)
		return nil
	})
}

// ArchiveCampaign — заказ кампании завершён (ordered → archived, R-19). Уже архивная — без
// изменений. Работает в транзакции вызывающего.
func (s *Service) ArchiveCampaign(ctx context.Context, orgID, campaignID uuid.UUID) error {
	const op = "Post.ArchiveCampaign"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Campaigns.GetForUpdate(ctx, orgID, campaignID)
		if err != nil {
			return notFound(err, "campaign")
		}
		from := c.Status
		if !c.Archive(s.Clock.Now()) {
			s.log.DebugContext(ctx, "campaign archive skipped", logx.KeyOp, op, KeyCampaignID, campaignID, "status", from)
			return nil
		}
		if err := s.Campaigns.Update(ctx, c); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: actionCampaignArchived, ObjectType: "campaign", ObjectID: c.ID.String(), ActorType: audit.ActorSystem,
			OrganizationID: &orgID, Data: map[string]any{"from": from, "to": c.Status},
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "campaign status changed", logx.KeyOp, op, KeyCampaignID, c.ID, "from", from, "to", c.Status)
		return nil
	})
}
