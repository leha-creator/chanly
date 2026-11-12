package httpapi

import (
	"context"

	"github.com/leha-creator/chanly/internal/apigen"
	"github.com/leha-creator/chanly/internal/notification/app"
	"github.com/leha-creator/chanly/internal/notification/domain"
)

func settingsAPI(s app.SettingsResult) apigen.AccountSettings {
	out := apigen.AccountSettings{Version: s.Version, HideNotificationPreview: s.HideNotificationPreview,
		QuietHours: apigen.QuietHours{Enabled: s.QuietHours.Enabled, Start: s.QuietHours.Start, End: s.QuietHours.End, Timezone: apigen.QuietHoursTimezone(s.QuietHours.Timezone)},
		Delivery:   []apigen.DeliveryPreference{}, Catalog: []apigen.DeliveryOption{}}
	for _, p := range s.Delivery {
		out.Delivery = append(out.Delivery, apigen.DeliveryPreference{Group: p.Group, Event: p.Event, Channel: apigen.DeliveryChannel(p.Channel), Enabled: p.Enabled})
	}
	for _, o := range s.Catalog {
		v := apigen.DeliveryOption{Group: o.Group, Event: o.Event, Channel: apigen.DeliveryChannel(o.Channel), Enabled: o.Enabled, Available: o.Available, Mandatory: o.Mandatory}
		if o.Reason != "" {
			reason := o.Reason
			v.Reason = &reason
		}
		out.Catalog = append(out.Catalog, v)
	}
	return out
}
func (h *Handlers) GetAccountSettings(ctx context.Context, _ apigen.GetAccountSettingsRequestObject) (apigen.GetAccountSettingsResponseObject, error) {
	id, err := user(ctx)
	if err != nil {
		return nil, err
	}
	s, err := h.svc.GetSettings(ctx, id)
	if err != nil {
		return nil, err
	}
	return apigen.GetAccountSettings200JSONResponse(settingsAPI(s)), nil
}
func (h *Handlers) UpdateAccountSettings(ctx context.Context, req apigen.UpdateAccountSettingsRequestObject) (apigen.UpdateAccountSettingsResponseObject, error) {
	id, err := user(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	p := domain.SettingsPatch{Version: b.Version, HideNotificationPreview: b.HideNotificationPreview}
	if q := b.QuietHours; q != nil {
		p.QuietHours = &domain.QuietHours{Enabled: q.Enabled, Start: q.Start, End: q.End, Timezone: string(q.Timezone)}
	}
	if b.Delivery != nil {
		for _, v := range *b.Delivery {
			p.Delivery = append(p.Delivery, domain.Preference{Group: v.Group, Event: v.Event, Channel: string(v.Channel), Enabled: v.Enabled})
		}
	}
	s, err := h.svc.UpdateSettings(ctx, id, p)
	if err != nil {
		return nil, err
	}
	return apigen.UpdateAccountSettings200JSONResponse(settingsAPI(s)), nil
}
