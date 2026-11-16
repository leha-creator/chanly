package rocketwork

import (
	"fmt"
	"math"
	"net/url"
	"time"
)

type ActVariables struct {
	ExecutorID      string   `json:"id_ispolnitelya"`
	OfferAcceptedOn string   `json:"data_akcepta"`
	Table           []ActRow `json:"table"`
}

type ActRow struct {
	OrderNumber string `json:"nomer_zakaza"`
	Publication string `json:"kanal_max_ssylka_na_pub"`
	Format      string `json:"format"`
	Period      string `json:"data_i_srok_razmeshcheniya"`
	Cost        string `json:"stoimost_uslug"`
}

type actMetadata struct {
	TaskVariables *ActVariables `json:"task_variables"`
}

func (a ActVariables) validate(amount int64) error {
	if a.ExecutorID == "" || len(a.ExecutorID) > 200 || len(a.Table) == 0 || len(a.Table) > 1000 {
		return fmt.Errorf("%w: act variables", ErrFormat)
	}
	if _, err := time.Parse(time.DateOnly, a.OfferAcceptedOn); err != nil {
		return fmt.Errorf("%w: act acceptance date", ErrFormat)
	}
	var total int64
	for _, row := range a.Table {
		u, err := url.Parse(row.Publication)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || row.OrderNumber == "" || row.Format == "" || row.Period == "" {
			return fmt.Errorf("%w: act row", ErrFormat)
		}
		cost, err := rublesToKopecks(row.Cost)
		if err != nil || cost <= 0 || total > math.MaxInt64-cost {
			return fmt.Errorf("%w: act cost", ErrFormat)
		}
		total += cost
	}
	if total != amount {
		return fmt.Errorf("%w: act total differs from payout", ErrFormat)
	}
	return nil
}
