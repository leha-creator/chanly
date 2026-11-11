package bootstrap

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	checkapp "github.com/leha-creator/chanly/internal/rocketworkcheck/app"
	checkdomain "github.com/leha-creator/chanly/internal/rocketworkcheck/domain"
	checkpg "github.com/leha-creator/chanly/internal/rocketworkcheck/postgres"
)

// The internal HTTP surface can operate only the single owner-approved run
// selected at deployment. It cannot accept a manifest, amount, recipient or grant.
type rwCheckHTTP struct {
	mu           sync.Mutex
	advance      func(context.Context, string, bool) error
	store        rwCheckViewStore
	runID, token string
	armingID     string
	log          *slog.Logger
}

type rwCheckViewStore interface {
	Run(context.Context, string) (checkdomain.Run, error)
	Grant(context.Context, string) (checkdomain.Grant, error)
	Status(context.Context, string) (map[string]any, error)
}

type rwCheckView struct {
	Available        bool   `json:"available"`
	State            string `json:"state"`
	RunID            string `json:"run_id,omitempty"`
	Recipient        string `json:"recipient,omitempty"`
	Amount           int64  `json:"amount_kopecks"`
	MaxCommission    int64  `json:"max_commission_kopecks"`
	ActualCommission *int64 `json:"actual_commission_kopecks,omitempty"`
	CanStart         bool   `json:"can_start"`
}

func (h *rwCheckHTTP) view(ctx context.Context) (rwCheckView, error) {
	r, err := h.store.Run(ctx, h.runID)
	if err != nil {
		return rwCheckView{}, err
	}
	status, err := h.store.Status(ctx, h.runID)
	if err != nil {
		return rwCheckView{}, err
	}
	var manifest checkdomain.Manifest
	if json.Unmarshal(r.Manifest, &manifest) != nil {
		return rwCheckView{}, checkdomain.ErrDenied
	}
	state := "ready"
	if r.ReservedAt != nil {
		state = "working"
	}
	if status["next_step"] == "reconcile" {
		state = "reconcile_required"
	}
	if status["next_step"] == "reconcile_only" || r.State == "released" {
		state = "stopped"
	}
	if r.State == "settled" {
		state = "paid"
		if r.ActualNominal != nil && *r.ActualNominal != r.Nominal || r.ActualFee != nil && *r.ActualFee > r.FeeCeiling {
			state = "discrepancy"
		}
	}
	grant, err := h.store.Grant(ctx, r.GrantID)
	if err != nil {
		return rwCheckView{}, err
	}
	if grant.Policy.ArmingID != h.armingID && r.State != "settled" && r.State != "released" {
		state = "stopped"
	}
	inn := manifest.Executor.INN
	if len(inn) > 4 {
		inn = strings.Repeat("•", len(inn)-4) + inn[len(inn)-4:]
	}
	v := rwCheckView{Available: true, State: state, RunID: r.ID, Recipient: inn,
		Amount: int64(r.Nominal), MaxCommission: int64(r.FeeCeiling), CanStart: state == "ready"}
	if r.ActualFee != nil {
		fee := int64(*r.ActualFee)
		v.ActualCommission = &fee
	}
	return v, nil
}

func (h *rwCheckHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.token)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/check" || r.URL.RawQuery != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && r.Header.Get("If-Match") != h.runID {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	startFailed := false
	if r.Method == http.MethodPost {
		v, err := h.view(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// A repeated start never creates a new run or resets a durable checkpoint.
		if v.CanStart {
			if err = h.advance(r.Context(), h.runID, true); err != nil {
				startFailed = true
				h.log.WarnContext(r.Context(), "diagnostic UI start requires verification", "run", h.runID)
			}
		}
	}
	v, err := h.view(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if startFailed && v.CanStart {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func (h *rwCheckHTTP) reconcile(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		h.mu.Lock()
		r, err := h.store.Run(ctx, h.runID)
		if err == nil && r.ReservedAt != nil && r.State != "settled" && r.State != "released" {
			// Advance reads authoritative provider evidence before each step and
			// refuses any second POST for an unknown committed intent.
			stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if h.advance(stepCtx, h.runID, true) != nil {
				h.log.DebugContext(ctx, "diagnostic UI run awaiting reconciliation", "run", h.runID)
			}
			cancel()
		}
		h.mu.Unlock()
	}
}

func serveRWCheck(ctx context.Context, vars map[string]string, id string, s *checkapp.Service, store *checkpg.Store, log *slog.Logger) int {
	if id == "" {
		id = vars["ROCKETWORK_CHECK_RUN_ID"]
	}
	if _, err := uuid.Parse(id); err != nil || len(vars["ROCKETWORK_CHECK_UI_TOKEN"]) < 32 || vars["ROCKETWORK_POLICY_DATABASE_URL"] != "" || s.ArmingID == "" {
		return 1
	}
	h := &rwCheckHTTP{advance: s.Advance, armingID: s.ArmingID, store: store, runID: id, token: vars["ROCKETWORK_CHECK_UI_TOKEN"], log: log}
	if _, err := h.view(ctx); err != nil {
		return 1
	}
	addr := vars["ROCKETWORK_CHECK_HTTP_ADDR"]
	if addr == "" {
		addr = ":8082"
	}
	workCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.reconcile(workCtx) }()
	err := serve(ctx, log, stopPolicy{}, namedServer{name: "rocketwork-check", srv: newHTTPServer(addr, h)})
	cancel()
	<-done
	if err != nil {
		return 1
	}
	return 0
}
