// Package reviewer owns the review stage: upstream evidence, verdict
// validation, read-only observation, and the Reviewing -> Completed lifecycle.
package reviewer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/stage"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
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

// Service is the review stage's public surface. Lifecycle, resume, and state
// transitions are hidden inside the package.
type Service interface {
	Review(context.Context, stage.Input, stage.PlanResult, stage.BuildResult, stage.VerificationResult) (stage.ReviewResult, error)
}

type service struct{ kit *stagekit.Kit }

func New(kit *stagekit.Kit) Service { return service{kit: kit} }

// Review resumes a durable result when present, otherwise assembles upstream
// evidence, runs the review turn, and publishes the verdict.
func (s service) Review(ctx context.Context, input stage.Input, plan stage.PlanResult, build stage.BuildResult, verification stage.VerificationResult) (stage.ReviewResult, error) {
	if result, ok, err := s.savedReview(ctx, input.TaskID, verification.AttemptID); err != nil || ok {
		return result, err
	}
	task, phase, err := s.beginReview(ctx, input.TaskID, plan.AttemptID, build.AttemptID, verification.AttemptID)
	if err != nil {
		return stage.ReviewResult{}, err
	}
	configured, err := s.kit.TaskConfig(ctx, task)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.ReviewResult{}, err
	}
	agent, ok := configured.Config.Agent(phase.Owner)
	if !ok {
		err = fmt.Errorf("agent %s not configured", phase.Owner)
		s.kit.Fail(ctx, phase, err)
		return stage.ReviewResult{}, err
	}
	before, err := workspace.Fingerprint(task)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.ReviewResult{}, err
	}
	data, err := s.evidence(ctx, task, plan)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.ReviewResult{}, err
	}
	systemPrompt, userPrompt, err := stagekit.RenderPrompts(
		agent.Name,
		agent.PromptEngineering.SystemContent, agent.PromptEngineering.System,
		agent.PromptEngineering.UserContent, agent.PromptEngineering.User,
		data, filepath.Dir(configured.ConfigPath),
		Instructions(),
	)
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.ReviewResult{}, err
	}
	harnessName := configured.Config.Defaults.CodingAgent
	turner := s.kit.AgentExec()
	turner.AgentDeadlineMS = configured.Config.Runtime.AgentDeadlineMS
	turner.JSONFixAttempts = configured.Config.Runtime.JSONFixAttempts
	turn, err := harness.RunTurn(ctx, turner, harness.TurnInput{
		TaskID: task.ID, RequestID: stagekit.RandomID(), Phase: phase, Role: phase.Name,
		HarnessName: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		RepoPath:     task.RepositoryPath,
		SessionDir:   filepath.Join(configured.TaskDir, "sessions", phase.Name, harnessName),
		SystemPrompt: systemPrompt, UserPrompt: userPrompt,
		ReadOnly: true, EnvelopeKind: "review", CorrectionSuffix: Instructions(),
		Validate: func(text string) (any, error) { return Validate(text) },
		Sink:     s.kit.Sink(task.ID, phase.ID, harnessName),
	})
	if err != nil {
		s.kit.Fail(ctx, phase, err)
		return stage.ReviewResult{}, err
	}
	return s.publishReview(ctx, task, phase, turn, before)
}

// evidence assembles the review prompt payload from upstream results.
func (s service) evidence(ctx context.Context, task store.Task, plan stage.PlanResult) (map[string]any, error) {
	checks, err := s.kit.DB().Checks(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	testChanges, err := s.kit.DB().TestChanges(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	comparisons, err := s.kit.DB().Comparisons(ctx, task.ID)
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
