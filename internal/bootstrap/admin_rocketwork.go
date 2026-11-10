package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	payoutdomain "github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/db"
	"github.com/leha-creator/chanly/internal/platform/inbound"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/money"
)

// Request files avoid putting names, document references and callback URLs in
// process arguments. Tokens come only from configuration and are never printed.
type rocketWorkAdminRequest struct {
	ID             uuid.UUID                      `json:"id"`
	PayoutID       uuid.UUID                      `json:"payout_id"`
	OrganizationID uuid.UUID                      `json:"organization_id"`
	DocumentID     uuid.UUID                      `json:"document_id"`
	Action         string                         `json:"action"`
	Reason         string                         `json:"reason"`
	Names          payoutdomain.RegistrationNames `json:"names"`
	Amount         int64                          `json:"amount_kopecks"`
	MaxCommission  int64                          `json:"max_commission_kopecks"`
	Destination    string                         `json:"destination"`
	ReceivedAt     time.Time                      `json:"received_at"`
	SubscriptionID int64                          `json:"subscription_id"`
	URI            string                         `json:"uri"`
	Streams        []string                       `json:"streams"`
	RetryLimit     int                            `json:"retry_limit"`
	From           time.Time                      `json:"from"`
	Through        time.Time                      `json:"through"`
}

func readRocketWorkAdminRequest(path string) (rocketWorkAdminRequest, error) {
	var request rocketWorkAdminRequest
	f, err := os.Open(path)
	if err != nil {
		return request, errors.New("cannot open Rocket Work request file")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 65537))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return request, errors.New("invalid Rocket Work request file")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return request, errors.New("invalid Rocket Work request file")
	}
	return request, nil
}

func runAdminRocketWork(ctx context.Context, cfg Config, log *slog.Logger, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	command := args[0]
	switch command {
	case "onboard", "cancel", "withdraw", "reconcile-withdrawal", "return", "subscriptions", "history":
	default:
		return errUsage
	}
	fs := flag.NewFlagSet("admin rocketwork "+command, flag.ContinueOnError)
	fs.SetOutput(out)
	file := fs.String("request", "", "JSON request file")
	email := fs.String("email", "", "senior administrator email")
	approverEmail := fs.String("approver-email", "", "second senior administrator email")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *file == "" || *email == "" {
		return errUsage
	}
	r, err := readRocketWorkAdminRequest(*file)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	in, err := newInfra(ctx, cfg, ModeAdmin, pool, log, nil)
	if err != nil {
		return err
	}
	m := newModules(cfg, in)
	c := &moneyCmd{m: m, users: accountpg.New(pool, log).Users(), log: log, out: out}
	admin, err := c.senior(ctx, *email)
	if err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Actor{Type: audit.ActorAdmin, UserID: &admin})
	approver := uuid.Nil
	if command == "withdraw" || command == "return" {
		if *approverEmail == "" {
			return payoutdomain.ErrSecondApproval
		}
		approver, err = c.senior(ctx, *approverEmail)
		if err != nil {
			return err
		}
		if approver == admin {
			return payoutdomain.ErrSecondApproval
		}
	}
	if command != "history" && command != "reconcile-withdrawal" && r.Reason == "" {
		return errUsage
	}
	switch command {
	case "onboard":
		if r.OrganizationID == uuid.Nil {
			return errUsage
		}
		err = m.txRunner.InTx(ctx, func(ctx context.Context) error {
			if err := m.payout.RequestOnboarding(ctx, r.OrganizationID, r.Action, r.Names); err != nil {
				return err
			}
			return m.payout.Audit.Record(ctx, audit.Entry{Action: "rocketwork.admin_onboarding", ObjectType: "organization", ObjectID: r.OrganizationID.String(), ActorType: audit.ActorAdmin, Reason: &r.Reason, Data: map[string]any{"action": r.Action}})
		})
	case "cancel":
		err = m.payout.RequestCancellation(ctx, r.PayoutID, admin, r.Reason)
	case "withdraw":
		dest := cfg.PayoutAppConfig().WithdrawalDestination
		if dest == nil {
			return payoutdomain.ErrProviderDisabled
		}
		_, err = m.payout.PrepareWithdrawal(ctx, r.ID, payoutdomain.WithdrawalTerms{Destination: *dest, Amount: money.Kopecks(r.Amount), MaxCommission: money.Kopecks(r.MaxCommission), AdminID: admin, ApproverID: approver, Reason: r.Reason})
		if err == nil {
			err = m.payout.ReconcileWithdrawal(ctx, r.ID)
		}
	case "reconcile-withdrawal":
		err = m.payout.ReconcileWithdrawal(ctx, r.ID)
	case "return":
		err = m.payout.RecordPayoutReturn(ctx, payoutdomain.ReturnEvidence{ID: r.ID, PayoutID: r.PayoutID, DocumentID: r.DocumentID, AdminID: admin, ApproverID: approver, Amount: money.Kopecks(r.Amount), Destination: r.Destination, Reason: r.Reason, ReceivedAt: r.ReceivedAt})
	case "subscriptions":
		api, ok := m.payout.Provider.(payoutdomain.EventProvider)
		if !ok {
			return payoutdomain.ErrProviderDisabled
		}
		spec := payoutdomain.SubscriptionSpec{URI: r.URI, Secret: cfg.RocketWork.WebhookSecret, Streams: r.Streams, RetryLimit: r.RetryLimit}
		if r.Action == "list" {
			rows, e := api.Subscriptions(ctx, spec)
			if e != nil {
				return e
			}
			for _, row := range rows {
				fmt.Fprintf(out, "subscription %d matches_request=%t\n", row.ID, row.Matches)
			}
		} else {
			if err = m.payout.Audit.Record(ctx, audit.Entry{Action: "rocketwork.subscription_requested", ObjectType: "rocketwork_command", ObjectID: r.ID.String(), ActorType: audit.ActorAdmin, Reason: &r.Reason, Data: map[string]any{"action": r.Action}}); err != nil {
				return err
			}
			_, err = m.payout.ChangeSubscription(ctx, r.ID, r.Action, r.SubscriptionID, spec)
		}
	case "history":
		recorder := inbound.NewRecorder(pool, m.txRunner, m.queue, in.clock, log)
		err = m.payout.RecoverPostbacks(ctx, recorder, r.From, r.Through)
	}
	if _, pending := job.PostponedFor(err); pending {
		fmt.Fprintln(out, "Command recorded; outcome requires reconciliation with the same id.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Command completed.")
	return nil
}
