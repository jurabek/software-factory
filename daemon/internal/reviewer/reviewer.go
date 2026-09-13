// Package reviewer owns review-stage entry and all upstream typed evidence.
package reviewer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

type Finding struct {
	Requirement string `json:"requirement"`
	Met         bool   `json:"met"`
	Evidence    string `json:"evidence"`
}

type Result struct {
	agentexec.Common
	Approved bool      `json:"approved"`
	Findings []Finding `json:"findings"`
	Blocking []string  `json:"blocking"`
}

func Validate(text string) (Result, error) {
	var value Result
	fields := append(append([]string{}, agentexec.CommonFields...), "approved", "findings", "blocking")
	if err := agentexec.DecodeExact(text, &value, fields, fields); err != nil {
		return value, err
	}
	if err := agentexec.ValidateCommon(value.Common); err != nil {
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
	return `Return exactly one JSON object: {` + agentexec.CommonInstructions() + `,"approved":true,"findings":[],"blocking":[]}. Put the human-readable report in report_markdown. Finding objects require "requirement", "met", and "evidence". A rejected review requires approved=false and a non-empty blocking array.`
}

// Diff is the repository patch under review.
type Diff struct {
	Files []string `json:"files"`
	Patch string   `json:"patch"`
}

// Evidence is the narrow store/git surface for review prompt data. Assembly
// of the prompt payload lives in Review; this interface only reads.
type Evidence interface {
	Checks(ctx context.Context, taskID string) ([]store.Check, error)
	TestChanges(ctx context.Context, taskID string) ([]store.TestChange, error)
	Comparisons(ctx context.Context, taskID string) ([]store.Comparison, error)
	ChangedFiles(ctx context.Context, taskID string) ([]string, error)
	Diff(ctx context.Context, taskID string) (Diff, error)
}

// Publisher is the narrow review-scoped bridge into orchestration. It begins
// the review phase, publishes the final payload atomically with message
// synchronization, verdict validation, and read-only enforcement, and
// resolves durable resume state. Checkpoint mechanics move to Pipeline later;
// the bridge stays review-scoped.
type Publisher interface {
	SavedReview(ctx context.Context, taskID, verificationAttemptID string) (pipeline.ReviewResult, bool, error)
	BeginReview(ctx context.Context, taskID, planAttemptID, buildAttemptID, verificationAttemptID string) (store.Task, store.Phase, error)
	PublishReview(ctx context.Context, task store.Task, phase store.Phase, payload, beforeFingerprint string) (pipeline.ReviewResult, error)
	FailReview(ctx context.Context, phase store.Phase, cause error)
}

// Deps supplies prompt configuration, evidence, turn execution, and review
// publication.
type Deps struct {
	Turner     agentexec.Deps
	Sinks      agentexec.SinkFactory
	Configurer agentexec.Configurer
	Publisher  Publisher
	Evidence   Evidence
	Git        factorygit.Runner
}

type Service struct{ deps Deps }

func New(deps Deps) Service { return Service{deps: deps} }

// Review resumes a durable result when present, otherwise assembles upstream
// evidence, runs the review turn, and publishes the payload.
func (s Service) Review(ctx context.Context, input pipeline.Input, plan pipeline.PlanResult, build pipeline.BuildResult, verification pipeline.VerificationResult) (pipeline.ReviewResult, error) {
	if result, ok, err := s.deps.Publisher.SavedReview(ctx, input.TaskID, verification.AttemptID); err != nil || ok {
		return result, err
	}
	task, phase, err := s.deps.Publisher.BeginReview(ctx, input.TaskID, plan.AttemptID, build.AttemptID, verification.AttemptID)
	if err != nil {
		return pipeline.ReviewResult{}, err
	}
	configured, err := s.deps.Configurer.TaskConfig(ctx, task)
	if err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	agent, ok := configured.Config.Agent(phase.Owner)
	if !ok {
		err = fmt.Errorf("agent %s not configured", phase.Owner)
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	before, err := workspace.Fingerprint(ctx, s.deps.Git, task)
	if err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	data := map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath, "Plan": plan.Payload}
	if data["Checks"], err = s.deps.Evidence.Checks(ctx, task.ID); err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	if data["TestChanges"], err = s.deps.Evidence.TestChanges(ctx, task.ID); err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	if data["Comparisons"], err = s.deps.Evidence.Comparisons(ctx, task.ID); err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	if data["ChangedFiles"], err = s.deps.Evidence.ChangedFiles(ctx, task.ID); err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	if data["Diff"], err = s.deps.Evidence.Diff(ctx, task.ID); err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	systemPrompt, userPrompt, err := agentexec.RenderPrompts(
		agent.Name,
		agent.PromptEngineering.SystemContent, agent.PromptEngineering.System,
		agent.PromptEngineering.UserContent, agent.PromptEngineering.User,
		data, filepath.Dir(configured.ConfigPath),
		filepath.Join(configured.TaskDir, "prompts", agent.Name),
		Instructions(),
	)
	if err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	harnessName := configured.Config.Defaults.CodingAgent
	turner := s.deps.Turner
	turner.AgentDeadlineMS = configured.Config.Runtime.AgentDeadlineMS
	turner.JSONFixAttempts = configured.Config.Runtime.JSONFixAttempts
	payload, err := agentexec.RunTurn(ctx, turner, agentexec.TurnInput{
		TaskID: task.ID, Phase: phase, Role: phase.Name,
		HarnessName: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		RepoPath: task.RepositoryPath,
		SessionDir: filepath.Join(configured.TaskDir, "sessions", phase.Name, harnessName),
		SystemPrompt: systemPrompt, UserPrompt: userPrompt,
		ReadOnly: true, EnvelopeKind: "review", CorrectionSuffix: Instructions(),
		Validate: func(text string) (any, error) { return Validate(text) },
		Sink:     s.deps.Sinks(task.ID, phase.ID, harnessName),
	})
	if err != nil {
		s.deps.Publisher.FailReview(ctx, phase, err)
		return pipeline.ReviewResult{}, err
	}
	return s.deps.Publisher.PublishReview(ctx, task, phase, payload, before)
}
