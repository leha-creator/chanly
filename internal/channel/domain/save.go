package domain

import (
	"time"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// UpdateProfileWithPrices validates a complete proposed state before changing the
// channel. Price eligibility is checked against both the original and final
// status, so changing RKN cannot bypass the active-channel price requirement.
func (c *Channel) UpdateProfileWithPrices(p ProfileUpdate, formats []Format, threshold int, now time.Time, minimum money.Kopecks) (StatusChange, error) {
	next := *c
	if p.Prices != nil {
		if err := next.SetPricesWithMinimum(*p.Prices, formats, now, minimum); err != nil {
			return StatusChange{}, err
		}
	}
	change, err := next.updateProfile(p, threshold, now)
	if err != nil {
		return StatusChange{}, err
	}
	if p.Prices != nil && next.Status == StatusActive && !next.HasOfferedPrice() {
		return StatusChange{}, ErrActiveRequiresPrice
	}
	*c = next
	return change, nil
}
