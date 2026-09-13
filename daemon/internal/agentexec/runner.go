package agentexec

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"uuid"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

const maxCapturedOutput = 64 << 10

// DB is the narrow persistence surface a single agent turn needs.
type DB interface {
	AgentSession(ctx context.Context, taskID, stageID string) (store.AgentSession, error)
	ReserveAgentSession(ctx context.Context, taskID string, value store.AgentSession) (store.AgentSession, error)
	BeginAgentInvocation(ctx context.Context, taskID, stageID, invocationID string) error
	FinalizeAgentInvocation(ctx context.Context, taskID, stageID, invocationID string, value store.AgentSession) error
	SaveEnvelope(ctx context.Context, id, taskID, phaseID, stageID, kind, payload string, valid bool, attempt int) error
}

// HarnessRegistry resolves the harness adapter for a turn.
type HarnessRegistry interface {
	Get(string) (harness.Harness, bool)
}

// Deps supplies the narrow collaborators for a turn. It carries runtime
// tuning and adapters, never the orchestrator.
type Deps struct {
	DB              DB
	Harnesses       HarnessRegistry
	Git             factorygit.Runner
	AgentDeadlineMS int
	JSONFixAttempts int
}

// Validate reports whether a harness response is a usable envelope.
type Validate func(string) (any, error)

// TurnInput describes one synchronous agent turn. Prompts arrive
// pre-rendered so prompt content stays owned by the calling stage.
type TurnInput struct {
	TaskID           string
	Phase            store.Phase
	Role             string
	HarnessName      string
	Model            string
	Thinking         string
	Color            string
	RepoPath         string
	SessionDir       string
	SystemPrompt     string
	UserPrompt       string
	ReadOnly         bool
	EnvelopeKind     string
	CorrectionSuffix string
	Validate         Validate
	Sink             harness.EventSink
}

// RunTurn executes the harness invocation loop for a single turn: persistent
// session reservation, read-only enforcement, session identity checks, usage
// accounting, envelope persistence, and JSON correction retries.
func RunTurn(ctx context.Context, deps Deps, input TurnInput) (string, error) {
	stageID := input.Role
	agentName := input.Role
	if input.Phase.Kind != "agent" && input.Phase.Owner != "" {
		stageID = input.Phase.Name
		agentName = input.Phase.Owner
	}
	adapter, ok := deps.Harnesses.Get(input.HarnessName)
	if !ok {
		return "", fmt.Errorf("harness %s unavailable", input.HarnessName)
	}
	storedSession, err := deps.DB.AgentSession(ctx, input.TaskID, stageID)
	if errors.Is(err, store.ErrNotFound) {
		storedSession, err = deps.DB.ReserveAgentSession(ctx, input.TaskID, store.AgentSession{StageID: stageID, AgentName: agentName, Role: agentName, Harness: input.HarnessName, Model: input.Model, Thinking: input.Thinking, Color: input.Color, HarnessSessionID: uuid.New().String(), SessionDirectory: input.SessionDir, AccountingComplete: true})
	}
	if err != nil {
		return "", err
	}
	if storedSession.Harness != input.HarnessName {
		return "", fmt.Errorf("role %s session belongs to harness %s", input.Role, storedSession.Harness)
	}
	request := harness.Request{CWD: input.RepoPath, Prompt: input.UserPrompt, SystemPrompt: input.SystemPrompt, Model: input.Model, Thinking: input.Thinking, SessionID: storedSession.HarnessSessionID, SessionDirectory: storedSession.SessionDirectory, RawOutputPath: filepath.Join(storedSession.SessionDirectory, "raw-output.jsonl"), DeadlineMS: deps.AgentDeadlineMS, Resume: storedSession.SessionReady}
	sessionReady := storedSession.SessionReady
	for attempt := 0; attempt <= deps.JSONFixAttempts; attempt++ {
		if attempt > 0 {
			request.Prompt = "Your previous final response was invalid: " + err.Error() + "\n" + input.CorrectionSuffix
		}
		var before string
		if input.ReadOnly {
			before, err = fingerprint(ctx, deps.Git, input.RepoPath)
			if err != nil {
				return "", err
			}
		}
		invocationID := uuid.New().String()
		if err := deps.DB.BeginAgentInvocation(ctx, input.TaskID, stageID, invocationID); err != nil {
			return "", err
		}
		result, runErr := Invoke(ctx, adapter, request, input.Sink)
		if input.ReadOnly {
			after, fingerprintErr := fingerprint(ctx, deps.Git, input.RepoPath)
			if fingerprintErr != nil {
				runErr = errors.Join(runErr, fingerprintErr)
			} else if before != after {
				runErr = errors.Join(runErr, fmt.Errorf("%s modified repository", input.Role))
			}
		}
		if result.SessionID == "" {
			result.SessionID = storedSession.HarnessSessionID
		}
		if result.SessionID != storedSession.HarnessSessionID {
			identityErr := fmt.Errorf("harness session identity changed from %s to %s", storedSession.HarnessSessionID, result.SessionID)
			if runErr != nil {
				runErr = errors.Join(runErr, identityErr)
			} else {
				runErr = identityErr
			}
		}
		sessionReady = sessionReady || result.SessionReady
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		finalizeErr := deps.DB.FinalizeAgentInvocation(cleanupCtx, input.TaskID, stageID, invocationID, store.AgentSession{StageID: stageID, AgentName: agentName, Role: agentName, Harness: input.HarnessName, Provider: result.Provider, Model: result.Model, Thinking: input.Thinking, Color: input.Color, HarnessSessionID: storedSession.HarnessSessionID, SessionDirectory: storedSession.SessionDirectory, SessionReady: sessionReady, NativeTranscriptPath: result.NativeTranscriptPath, ContextTokens: result.ContextTokens, ContextWindow: result.ContextWindow, Usage: persistedUsage(result.Usage), Cost: result.Usage.Cost, AccountingComplete: result.AccountingComplete})
		cancel()
		if finalizeErr != nil {
			return "", finalizeErr
		}
		request.Resume = sessionReady
		if runErr != nil {
			return "", runErr
		}
		_, validationErr := input.Validate(result.Text)
		valid := validationErr == nil
		tail := result.Text
		if len(tail) > maxCapturedOutput {
			tail = tail[len(tail)-maxCapturedOutput:]
		}
		stored := tail
		if valid {
			stored = result.Text
		}
		if err := deps.DB.SaveEnvelope(ctx, uuid.New().String(), input.TaskID, input.Phase.ID, stageID, input.EnvelopeKind, stored, valid, attempt+1); err != nil {
			return "", err
		}
		if valid {
			return result.Text, nil
		}
		err = validationErr
	}
	return "", fmt.Errorf("%s envelope invalid after corrections: %w", input.Role, err)
}

func fingerprint(ctx context.Context, runner factorygit.Runner, repoPath string) (string, error) {
	if repoPath == "" {
		return "", nil
	}
	return factorygit.Fingerprint(ctx, runner, repoPath)
}

func persistedUsage(value harness.Usage) session.Usage {
	return session.Usage{Input: value.Input, Output: value.Output, CacheRead: value.CacheRead, CacheWrite: value.CacheWrite, Reasoning: value.Reasoning, TotalTokens: value.TotalTokens}
}
