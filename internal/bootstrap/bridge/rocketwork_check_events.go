package bridge

import (
	"context"
	"strconv"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	checkapp "github.com/leha-creator/chanly/internal/rocketworkcheck/app"
)

type CheckEventRouter struct {
	Events       checkapp.Events
	AccountAlias string
	API          interface {
		GetTask(context.Context, int64) (rocketwork.TaskSnapshot, error)
	}
}

func (r CheckEventRouter) RouteDiagnostic(ctx context.Context, external string) (bool, error) {
	id, err := strconv.ParseInt(external, 10, 64)
	if err != nil || id <= 0 {
		return false, nil
	}
	task, err := r.API.GetTask(ctx, id)
	if err != nil {
		return false, err
	}
	if task.ID != id || task.Type != "OneTime" {
		return false, nil
	}
	return r.Events.Route(ctx, r.AccountAlias, task.ExternalID, id)
}
