// Package reviewer owns the review stage: upstream evidence, verdict
// validation, read-only observation, and the Reviewing -> Completed lifecycle.
package reviewer

import (
	"context"
	"fmt"
	"strings"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

type Finding struct {
	Requirement string `json:"requirement"`
	Met         bool   `json:"met"`
	Evidence    string `json:"evidence"`
}

type Result struct {
	stage.Common
	Approved bool      `json:"approved"`
	Findings []Finding `json:"findings"`
	Blocking []string  `json:"blocking"`
}

func Validate(text string) (Result, error) {
	var value Result
	fields := append(append([]string{}, stage.CommonFields...), "approved", "findings", "blocking")
	if err := stage.DecodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := stage.ValidateCommon(value.Common); err != nil {
		return value, err
	}
	if value.Findings == nil || value.Blocking == nil {
		return value, fmt.Errorf("review findings and blocking arrays are required")
	}
	for _, finding := range value.Findings {
		if strings.TrimSpace(finding.Requirement) == "" || strings.TrimSpace(finding.Evidence) == "" {
			return value, fmt.Errorf("review finding requires requirement and evidence")
		}
		if !finding.Met && value.Approved {
			return value, fmt.Errorf("approved review has unmet finding")
		}
	}
	if value.Approved && len(value.Blocking) > 0 {
		return value, fmt.Errorf("approved review has blocking findings")
	}
	if !value.Approved && len(value.Blocking) == 0 {
		return value, fmt.Errorf("rejected review requires blocking findings")
	}
	return value, nil
}

func Instructions() string {
	return `Return exactly one JSON object: {` + stage.CommonInstructions() + `,"approved":true,"findings":[],"blocking":[]}. Put the human-readable report in report_markdown. Finding objects require "requirement", "met", and "evidence". A rejected review requires approved=false and a non-empty blocking array.`
}

// Diff is the repository patch under review.
type Diff struct {
	Files []string `json:"files"`
	Patch string   `json:"patch"`
}

type service struct {
	kit   *stagekit.Kit
	tasks *task.Service
}

func New(kit *stagekit.Kit, tasks *task.Service) service { return service{kit: kit, tasks: tasks} }

// Review resumes a durable result when present, otherwise assembles upstream
// evidence, runs the review turn, and publishes the verdict.
func (s service) Review(ctx context.Context, input stage.Input, plan stage.PlanResult, build stage.BuildResult, verification stage.VerificationResult) (stage.ReviewResult, error) {
	return s.kit.RunAgentStage(ctx, input.TaskID, stagekit.AgentStage[stage.ReviewResult]{
		Entry: stagekit.Entry{Kind: "review", Requires: []stagekit.Upstream{
			{Kind: "plan", AttemptID: plan.AttemptID}, {Kind: "build", AttemptID: build.AttemptID}, {Kind: "verify", AttemptID: verification.AttemptID},
		}, Enter: stagekit.Reviewing},
		After: verification.AttemptID,
		Role:  "review", ReadOnly: true, Instructions: Instructions(),
		Data: func(ctx context.Context, task store.Task) (map[string]any, error) {
			return s.evidence(ctx, task, plan)
		},
		Validate: func(_ store.Task, text string) (any, error) { return Validate(text) },
		Finalize: func(_ context.Context, _ store.Task, _ store.Phase, turn harness.TurnResult) (stagekit.Completion, error) {
			review, err := Validate(turn.Payload)
			if err != nil {
				return stagekit.Completion{}, err
			}
			if !review.Approved {
				return stagekit.Completion{To: stagekit.Blocked, Status: "failed", Cause: fmt.Errorf("reviewer rejected implementation")}, nil
			}
			return stagekit.Completion{To: stagekit.Completed, Status: "success"}, nil
		},
		Result: func(_ store.Task, phase store.Phase, payload string) (stage.ReviewResult, error) {
			review, err := Validate(payload)
			if err != nil {
				return stage.ReviewResult{}, err
			}
			return stage.ReviewResult{Payload: payload, AttemptID: phase.ID, SnapshotID: phase.OutputSnapshot, Approved: review.Approved}, nil
		},
	})
}

// evidence assembles the review prompt payload from upstream results.
func (s service) evidence(ctx context.Context, task store.Task, plan stage.PlanResult) (map[string]any, error) {
	checks, err := s.kit.Checks(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	testChanges, err := s.kit.TestChanges(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	comparisons, err := s.kit.Comparisons(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	changedFiles, err := workspace.ChangedFiles(task, true)
	if err != nil {
		return nil, err
	}
	files, err := factorygit.ChangedFiles(task.RepositoryPath, workspace.ReviewBase(task))
	if err != nil {
		return nil, err
	}
	patch, err := factorygit.Diff(task.RepositoryPath, workspace.ReviewBase(task))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath,
		"Plan": plan.Payload, "Checks": checks, "TestChanges": testChanges, "Comparisons": comparisons,
		"ChangedFiles": changedFiles, "Diff": Diff{Files: files, Patch: patch},
	}, nil
}
