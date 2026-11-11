package app

import (
	"context"
	"log/slog"
)

type EventRegistry interface {
	EventOwner(context.Context, string, string, int64) (string, error)
}
type Events struct {
	Registry EventRegistry
	Log      *slog.Logger
}

// Route only consumes a centrally-owned diagnostic task after an authoritative
// provider GET. A callback does not settle money or authorize any next action.
// Reconciliation uses the isolated runner's polling command, even without callbacks.
func (e Events) Route(ctx context.Context, alias, externalID string, taskID int64) (bool, error) {
	run, err := e.Registry.EventOwner(ctx, alias, externalID, taskID)
	if err != nil {
		return false, err
	}
	if run == "" {
		return false, nil
	}
	e.Log.InfoContext(ctx, "diagnostic event retained for runner polling", "run", run, "task_id", taskID)
	return true, nil
}
