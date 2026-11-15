package domain

import (
	"math"
	"slices"
	"time"
)

// Флаги аномалий метрик (04 §3, 05 §2.7).
const (
	FlagSubscribersSpike    = "subscribers_spike"
	FlagViewsDisproportion  = "views_disproportion"
	metricsMessagesPerPage  = 100
	metricsDefaultPageLimit = 5
)

// msk — сутки снимка метрик считаются по Москве (R-19).
var msk = time.FixedZone("MSK", 3*60*60)

// MetricsDate — сутки снимка по Москве как дата в полночь UTC.
func MetricsDate(t time.Time) time.Time {
	y, m, d := t.In(msk).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// MetricsDayStart — начало суток day (дата из MetricsDate) по Москве.
func MetricsDayStart(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, msk)
}

// MessageStat — сообщение канала для расчёта метрик. Текст не читается и не хранится.
type MessageStat struct {
	ID string
	At time.Time
	// Views — stat.views; nil, если MAX поле не вернул (Q-08).
	Views   *int
	FromBot bool
}

// MetricsConfig — окна и пороги расчёта (05 §2.7, R-14, R-15).
type MetricsConfig struct {
	// Window — окно чтения сообщений (30 суток).
	Window time.Duration
	// MinAge — сообщение входит в среднее просмотров, если ему больше суток.
	MinAge time.Duration
	// ViewsSample — сколько последних сообщений входит в среднее (100).
	ViewsSample int
	// PostsWindow — окно частоты публикаций (28 суток).
	PostsWindow time.Duration
	// MaxPages — сколько страниц по 100 сообщений читать (5).
	MaxPages int
	// SubscribersSpikeBP — рост подписчиков за сутки больше этой доли — флаг (METRICS_SUBSCRIBERS_SPIKE_BP, 2000).
	SubscribersSpikeBP int
	// ViewsMinBP — avg_views меньше этой доли подписчиков — флаг (METRICS_VIEWS_MIN_BP, 100).
	ViewsMinBP int
}

// WithDefaults — значения по умолчанию вместо нулевых.
func (c MetricsConfig) WithDefaults() MetricsConfig {
	if c.Window == 0 {
		c.Window = 30 * 24 * time.Hour
	}
	if c.MinAge == 0 {
		c.MinAge = 24 * time.Hour
	}
	if c.ViewsSample == 0 {
		c.ViewsSample = metricsMessagesPerPage
	}
	if c.PostsWindow == 0 {
		c.PostsWindow = 28 * 24 * time.Hour
	}
	if c.MaxPages == 0 {
		c.MaxPages = metricsDefaultPageLimit
	}
	if c.SubscribersSpikeBP == 0 {
		c.SubscribersSpikeBP = 2000
	}
	if c.ViewsMinBP == 0 {
		c.ViewsMinBP = 100
	}
	return c
}

// PageSize — сколько сообщений просить у MAX за раз.
func (c MetricsConfig) PageSize() int { return metricsMessagesPerPage }

// MetricsSnapshot — суточный снимок метрик (channel_metrics). Пустые поля — значения нет.
type MetricsSnapshot struct {
	Date             time.Time
	SubscribersCount int
	// SubscribersChange is derived for transport from consecutive calendar-day
	// totals. It is never persisted and is unknown without yesterday's snapshot.
	SubscribersChange *int64
	AvgViews          *int
	EngagementBP      *int
	PostsPerWeek      *float64
	AnomalyFlags      []string
	// NewViews counts available-post increments over the actual observed period.
	// Unknown includes the baseline, incomplete counters, resets and skipped days.
	NewViews             *int64
	ObservedAt           *time.Time
	ViewsPeriodStartedAt *time.Time
}

// ComputeMetrics считает снимок по 05 §2.7 и R-14, R-15. Сообщения бота платформы
// входят в расчёт. Нет сообщений (Q-04) или ни у одного нет stat.views (Q-08) —
// avg_views, engagement_bp и posts_per_week пусты, каталог показывает только подписчиков.
func ComputeMetrics(subscribers int, messages []MessageStat, prev *MetricsSnapshot, now time.Time, cfg MetricsConfig) MetricsSnapshot {
	cfg = cfg.WithDefaults()
	subscribers = max(subscribers, 0)
	s := MetricsSnapshot{Date: MetricsDate(now), SubscribersCount: subscribers, AnomalyFlags: []string{}}

	inWindow := make([]MessageStat, 0, len(messages))
	anyViews := false
	for _, m := range messages {
		if m.At.After(now) || now.Sub(m.At) > cfg.Window {
			continue
		}
		inWindow = append(inWindow, m)
		anyViews = anyViews || m.Views != nil
	}
	if anyViews {
		// Новые первыми: в среднее идут последние сообщения старше суток.
		slices.SortFunc(inWindow, func(a, b MessageStat) int { return b.At.Compare(a.At) })
		sum, n := 0, 0
		posts := 0
		for _, m := range inWindow {
			if now.Sub(m.At) <= cfg.PostsWindow {
				posts++
			}
			if m.Views == nil || now.Sub(m.At) <= cfg.MinAge || n >= cfg.ViewsSample {
				continue
			}
			sum += max(*m.Views, 0)
			n++
		}
		weeks := cfg.PostsWindow.Hours() / (24 * 7)
		ppw := math.Round(float64(posts)/weeks*100) / 100
		s.PostsPerWeek = &ppw
		if n > 0 {
			avg := sum / n
			s.AvgViews = &avg
			if subscribers > 0 {
				eng := int(int64(avg) * 10000 / int64(subscribers))
				s.EngagementBP = &eng
			}
		}
	}

	if prev != nil && prev.SubscribersCount > 0 &&
		int64(subscribers-prev.SubscribersCount)*10000 > int64(prev.SubscribersCount)*int64(cfg.SubscribersSpikeBP) {
		s.AnomalyFlags = append(s.AnomalyFlags, FlagSubscribersSpike)
	}
	if s.AvgViews != nil {
		avg := int64(*s.AvgViews)
		if avg > int64(subscribers) || avg*10000 < int64(subscribers)*int64(cfg.ViewsMinBP) {
			s.AnomalyFlags = append(s.AnomalyFlags, FlagViewsDisproportion)
		}
	}
	return s
}
