package rocketwork

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

func (c *Client) CompleteTask(ctx context.Context, id int64) (TaskSnapshot, error) {
	return c.taskAction(ctx, id, "complete")
}
func (c *Client) SignTaskAct(ctx context.Context, id int64) (TaskSnapshot, error) {
	return c.taskAction(ctx, id, "sign_act")
}
func (c *Client) PayTask(ctx context.Context, id int64) (TaskSnapshot, error) {
	return c.taskAction(ctx, id, "pay")
}
func (c *Client) CancelTask(ctx context.Context, id int64) (TaskSnapshot, error) {
	return c.taskAction(ctx, id, "cancel")
}

func (c *Client) taskAction(ctx context.Context, id int64, action string) (TaskSnapshot, error) {
	before, err := c.GetTask(ctx, id)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if before.Type != "OneTime" {
		return TaskSnapshot{}, ErrFormat
	}
	allowed := false
	switch action {
	case "complete":
		allowed = before.Status == "inwork" || before.Status == "accepting"
	case "sign_act":
		allowed = before.Status == "accepting" && before.ActRequired != nil && *before.ActRequired
	case "pay":
		allowed = before.Status == "completed"
	case "cancel":
		allowed = (before.Status == "draft" || before.Status == "inwork") && before.CustomerSigned != nil && !*before.CustomerSigned && before.ExecutorSigned != nil && !*before.ExecutorSigned
	}
	if !allowed {
		c.log.WarnContext(ctx, "rocketwork task action not allowed", "action", action)
		return TaskSnapshot{}, fmt.Errorf("%w: task action not allowed", ErrFormat)
	}
	var response struct {
		Task *taskDTO `json:"task"`
	}
	if err := c.write(ctx, "Task."+action, http.MethodPatch, "/api/tasks/"+strconv.FormatInt(id, 10)+"/"+action, nil, &response); err != nil {
		return TaskSnapshot{}, err
	}
	if response.Task == nil {
		return TaskSnapshot{}, ErrUncertain
	}
	after, err := response.Task.snapshot()
	if err != nil || after.ID != id || after.ExternalID != before.ExternalID || after.AmountKopecks != before.AmountKopecks {
		return TaskSnapshot{}, ErrUncertain
	}
	// Inconsistent examples do not prove the effect. Workflow must GET again.
	return after, nil
}
