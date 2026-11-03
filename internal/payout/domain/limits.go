package domain

import (
	"time"

	"github.com/leha-creator/chanly/internal/platform/money"
)

// FastPollWindow — первый час опрашиваем часто, затем редко (05 §4.2, R-10).
const FastPollWindow = time.Hour

// PollDelay — задержка следующего опроса объекта, отправленного в since (R-10).
func PollDelay(since, now time.Time, fast, slow time.Duration) time.Duration {
	if now.Sub(since) < FastPollWindow {
		return fast
	}
	return slow
}

// DepositNeed — сколько должно лежать на депозите Рокет Ворк (R-12, 05 §4.3): начислено
// блогерам-получателям Рокет Ворк и ещё не выплачено плюс цена их размещений в published и removed.
func DepositNeed(payable, inFlight money.Kopecks) money.Kopecks { return payable + inFlight }

// DepositMismatch — расхождение остатка депозита в журнале и в сервисе больше допуска (06 §2.4).
func DepositMismatch(ledger, api, tolerance money.Kopecks) bool {
	d := ledger - api
	if d < 0 {
		d = -d
	}
	return d > tolerance
}

// NPDWarnShareBP — порог предупреждения о годовом лимите самозанятого: 90 % (R-13).
const NPDWarnShareBP = 9000

// NPDWarning — сумма выплат за год вместе с новой достигла 90 % лимита (06 §4).
func NPDWarning(yearSum, amount, limit money.Kopecks) bool {
	if limit <= 0 {
		return false
	}
	return (yearSum+amount)*10000 >= limit*NPDWarnShareBP
}

var moscow = time.FixedZone("MSK", 3*60*60)

// YearBounds — календарный год по Москве, в котором лежит t: [from, to).
func YearBounds(t time.Time) (from, to time.Time) {
	y := t.In(moscow).Year()
	from = time.Date(y, time.January, 1, 0, 0, 0, 0, moscow)
	return from.UTC(), from.AddDate(1, 0, 0).UTC()
}
