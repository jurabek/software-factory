package planner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

var ErrStalePlan = errors.New("plan digest is stale")

// Approve validates and records approval of the latest durable plan, then
// publishes the command that lets the orchestrator continue the pipeline.
func (s service) Approve(ctx context.Context, taskID, actor, expectedDigest string) error {
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest == "" {
		return fmt.Errorf("plan_digest is required")
	}
	task, err := s.kit.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.State != string(stagekit.AwaitingApproval) {
		return store.ErrConflict
	}
	stageDef, err := s.kit.StageByKind(task, "plan")
	if err != nil {
		return err
	}
	payload, err := s.kit.DB().ValidEnvelope(ctx, taskID, stageDef.ID)
	if err != nil {
		return err
	}
	plan, err := Validate(payload)
	if err != nil || len(plan.Questions) > 0 {
		return store.ErrConflict
	}
	reportDigest := ""
	artifacts, err := s.kit.DB().Artifacts(ctx, taskID)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if artifact.AttemptID != "" && artifact.Type == "plan_report" {
			reportDigest = artifact.Digest
		}
	}
	currentDigest := stagekit.PlanApprovalDigest(payload, reportDigest)
	if expectedDigest != currentDigest {
		return ErrStalePlan
	}
	event := store.Event{
		ID: stagekit.RandomID(), TaskID: taskID, Kind: session.KindCustom, Name: "task_approved",
		Payload: session.CustomPayload{CustomType: "task_approved", Data: session.BoundedJSON(map[string]any{
			"task_id": taskID, "plan_digest": currentDigest, "actor": actor,
		})},
		Display:          session.Display{Role: "system", Status: "success", Title: "Plan approved"},
		AvailableActions: []string{"pause", "abort"}, StartedAt: time.Now().UTC(),
	}
	if err = s.kit.DB().SetApproval(ctx, taskID, currentDigest, actor); err != nil {
		return err
	}
	if _, err = s.kit.DB().AppendEvent(ctx, s.kit.TaskDir(taskID), event); err != nil {
		return err
	}
	return s.events.Publish(ctx, taskID, store.TaskApproved)
}
