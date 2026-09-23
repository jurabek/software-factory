package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"uuid"

	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

const maxCapturedOutput = 64 << 10

// Deps supplies the narrow collaborators for a turn. It carries runtime tuning
// and adapters, never the orchestrator. NativeReader is optional and backs
// restart reconciliation for turns recovered without a live session.
type Deps struct {
	DB              *store.Store
	Harnesses       Registry
	NativeReader    NativeReader
	AgentDeadlineMS int
	JSONFixAttempts int
}

// Validate reports whether a harness response is a usable envelope.
type Validate func(string) (any, error)

// TurnInput describes one synchronous agent turn. Prompts arrive pre-rendered
// so prompt content stays owned by the calling stage.
type TurnInput struct {
	TaskID           string
	RequestID        string
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
	Sink             EventSink
	// OnDispatch runs once, after the invocation is recorded but before the
	// harness is prompted. It lets a caller durably record a message delivery
	// in the same window as the invocation marker.
	OnDispatch func(ctx context.Context, invocationID string) error
}

// RunTurn executes the single shared harness invocation loop: persistent
// session open, read-only enforcement, session identity checks, usage
// accounting from native stats, report resolution from the native entry,
// envelope persistence, and JSON correction retries. Stages supply only
// prompts and an envelope validator.
func RunTurn(ctx context.Context, deps Deps, input TurnInput) (TurnResult, error) {
	stageID := input.Role
	agentName := input.Role
	if input.Phase.Kind != "agent" && input.Phase.Owner != "" {
		stageID = input.Phase.Name
		agentName = input.Phase.Owner
	}
	adapter, ok := deps.Harnesses.Get(input.HarnessName)
	if !ok {
		return TurnResult{}, fmt.Errorf("harness %s unavailable", input.HarnessName)
	}
	storedSession, err := deps.DB.AgentSessions.Get(ctx, input.TaskID, stageID)
	if errors.Is(err, store.ErrNotFound) {
		storedSession, err = deps.DB.AgentSessions.Reserve(ctx, input.TaskID, store.AgentSession{StageID: stageID, AgentName: agentName, Role: agentName, Harness: input.HarnessName, Model: input.Model, Thinking: input.Thinking, Color: input.Color, HarnessSessionID: uuid.New().String(), SessionDirectory: input.SessionDir})
	}
	if err != nil {
		return TurnResult{}, err
	}
	if storedSession.Harness != input.HarnessName {
		return TurnResult{}, fmt.Errorf("role %s session belongs to harness %s", input.Role, storedSession.Harness)
	}
	if storedSession.PendingInvocationID != "" {
		payload, reused, reconcileErr := reconcilePendingTurn(ctx, deps, input, stageID, storedSession)
		if reconcileErr != nil {
			return TurnResult{}, reconcileErr
		}
		if reused {
			return payload, nil
		}
		storedSession.PendingInvocationID = ""
	}
	native, err := adapter.Open(ctx, SessionSpec{
		CWD: input.RepoPath, Model: input.Model, Thinking: input.Thinking,
		SystemPrompt: input.SystemPrompt, SessionID: storedSession.HarnessSessionID,
		SessionDirectory: storedSession.SessionDirectory,
	})
	if err != nil {
		return TurnResult{}, err
	}
	defer native.Close()
	sessionReady := storedSession.SessionReady
	forkAt := ""
	if input.Phase.ForkNative {
		forkAt = input.Phase.NativeBaseEntryID
	} else if input.Phase.NativeBaseEntryID == "" {
		// Record the attempt's input checkpoint so a later exact retry can fork
		// the native session back to it.
		if err = deps.DB.Phases.SetNativeBase(ctx, input.TaskID, input.Phase.ID, storedSession.LastEntryID); err != nil {
			return TurnResult{}, err
		}
	}
	for attempt := 0; attempt <= deps.JSONFixAttempts; attempt++ {
		prompt := Prompt{RequestID: input.RequestID, Attempt: attempt + 1, Text: input.UserPrompt, DeadlineMS: deps.AgentDeadlineMS}
		if attempt == 0 {
			prompt.ForkAtEntryID = forkAt
		}
		if attempt > 0 {
			prompt.Text = "Your previous final response was invalid: " + err.Error() + "\n" + input.CorrectionSuffix
		}
		var before string
		if input.ReadOnly {
			before, err = fingerprint(input.RepoPath)
			if err != nil {
				return TurnResult{}, err
			}
		}
		invocationID := uuid.New().String()
		if err := deps.DB.AgentSessions.BeginInvocation(ctx, input.TaskID, stageID, invocationID, input.RequestID, input.Phase.ID); err != nil {
			return TurnResult{}, err
		}
		if input.OnDispatch != nil {
			if err := input.OnDispatch(ctx, invocationID); err != nil {
				return TurnResult{}, err
			}
		}
		result, runErr := invoke(ctx, native, prompt, input.Sink)
		if input.ReadOnly {
			after, fingerprintErr := fingerprint(input.RepoPath)
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
		reconcileNative(ctx, native, input.RequestID, &result)
		correlateNative(ctx, deps.DB, native, input.TaskID, input.RequestID)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		finalizeErr := deps.DB.AgentSessions.FinalizeInvocation(cleanupCtx, input.TaskID, stageID, invocationID, store.AgentSession{StageID: stageID, AgentName: agentName, Role: agentName, Harness: input.HarnessName, Provider: result.Provider, Model: result.Model, Thinking: input.Thinking, Color: input.Color, HarnessSessionID: storedSession.HarnessSessionID, SessionDirectory: storedSession.SessionDirectory, SessionReady: sessionReady, NativeTranscriptPath: result.NativeTranscriptPath, ContextTokens: result.ContextTokens, ContextWindow: result.ContextWindow, LastEntryID: result.LeafEntryID, Usage: persistedUsage(result.Usage), Cost: result.Usage.Cost})
		cancel()
		if finalizeErr != nil {
			return TurnResult{}, finalizeErr
		}
		if runErr != nil {
			return TurnResult{}, runErr
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
		if err := deps.DB.Envelopes.Save(ctx, uuid.New().String(), input.TaskID, input.Phase.ID, stageID, input.EnvelopeKind, stored, valid, attempt+1); err != nil {
			return TurnResult{}, err
		}
		if valid {
			return TurnResult{Payload: result.Text, ReportText: result.ReportText, ReportEntryID: result.ReportEntryID}, nil
		}
		err = validationErr
	}
	return TurnResult{}, fmt.Errorf("%s envelope invalid after corrections: %w", input.Role, err)
}

// reconcilePendingTurn resolves an in-flight turn that survived a restart. When
// the native session already holds a valid final response for the pending
// request, the turn is settled and returned instead of re-prompting. Otherwise
// the marker is released so the turn is re-driven.
func reconcilePendingTurn(ctx context.Context, deps Deps, input TurnInput, stageID string, stored store.AgentSession) (TurnResult, bool, error) {
	if stored.PendingPhaseID != input.Phase.ID {
		return TurnResult{}, false, releaseInvocation(ctx, deps, input.TaskID, stageID, stored.PendingInvocationID)
	}
	ref := SessionRef{ID: stored.HarnessSessionID, Directory: stored.SessionDirectory}
	result := Result{SessionID: stored.HarnessSessionID, Provider: stored.Provider, Model: stored.Model, SessionReady: true}
	if deps.NativeReader == nil {
		return TurnResult{}, false, releaseInvocation(ctx, deps, input.TaskID, stageID, stored.PendingInvocationID)
	}
	reconcileNative(ctx, readerSource{reader: deps.NativeReader, ref: ref}, stored.PendingRequestID, &result)
	if strings.TrimSpace(result.Text) == "" {
		return TurnResult{}, false, releaseInvocation(ctx, deps, input.TaskID, stageID, stored.PendingInvocationID)
	}
	if _, err := input.Validate(result.Text); err != nil {
		return TurnResult{}, false, releaseInvocation(ctx, deps, input.TaskID, stageID, stored.PendingInvocationID)
	}
	err := deps.DB.AgentSessions.FinalizeInvocation(ctx, input.TaskID, stageID, stored.PendingInvocationID, store.AgentSession{
		StageID: stageID, AgentName: stored.AgentName, Role: stored.Role, Harness: stored.Harness,
		Provider: result.Provider, Model: result.Model, Thinking: stored.Thinking, Color: stored.Color,
		HarnessSessionID: stored.HarnessSessionID, SessionDirectory: stored.SessionDirectory,
		SessionReady: true, NativeTranscriptPath: stored.NativeTranscriptPath,
		ContextTokens: result.ContextTokens, ContextWindow: result.ContextWindow,
		LastEntryID: result.LeafEntryID, Usage: persistedUsage(result.Usage), Cost: result.Usage.Cost,
	})
	if err != nil {
		return TurnResult{}, false, err
	}
	if err = deps.DB.Envelopes.Save(ctx, uuid.New().String(), input.TaskID, input.Phase.ID, stageID, input.EnvelopeKind, result.Text, true, 1); err != nil {
		return TurnResult{}, false, err
	}
	return TurnResult{Payload: result.Text, ReportText: result.ReportText, ReportEntryID: result.ReportEntryID}, true, nil
}

func releaseInvocation(ctx context.Context, deps Deps, taskID, stageID, invocationID string) error {
	if err := deps.DB.AgentSessions.ClearInvocation(ctx, taskID, stageID, invocationID); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// nativeSource is the narrow native-session surface report/accounting
// reconciliation needs, satisfied by a live Session or a disk reader.
type nativeSource interface {
	Stats(context.Context) (Stats, error)
	Report(context.Context, string) (Report, bool, error)
}

type readerSource struct {
	reader NativeReader
	ref    SessionRef
}

func (s readerSource) Stats(ctx context.Context) (Stats, error) {
	return s.reader.Stats(ctx, s.ref)
}

func (s readerSource) Report(ctx context.Context, requestID string) (Report, bool, error) {
	return s.reader.Report(ctx, s.ref, requestID)
}

// correlateNative backfills the native entry reference for the events produced
// by a request. The native session is authoritative, so the read path resolves
// payloads from these references.
func correlateNative(ctx context.Context, db *store.Store, native Session, taskID, requestID string) {
	if requestID == "" || taskID == "" {
		return
	}
	entries, err := native.Entries(ctx)
	if err != nil {
		return
	}
	requestEntries := requestSubtree(entries, requestID)
	if len(requestEntries) == 0 {
		return
	}
	events, err := db.Events.ByRequest(ctx, taskID, requestID)
	if err != nil {
		return
	}
	links := matchNativeEvents(entries, requestEntries, events)
	if len(links) == 0 {
		return
	}
	_ = db.Events.SetNativeEntries(ctx, taskID, links)
}

// requestSubtree resolves the native entry ids belonging to a factory request,
// keyed by id, using the factory-request custom entry as the subtree root.
func requestSubtree(entries []NativeEntry, requestID string) map[string]bool {
	root := ""
	for _, entry := range entries {
		if entry.Type != "custom" || len(entry.Data) == 0 {
			continue
		}
		var data struct {
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(entry.Data, &data) == nil && data.RequestID == requestID {
			root = entry.ID
			break
		}
	}
	if root == "" {
		return nil
	}
	children := map[string][]string{}
	for _, entry := range entries {
		if entry.ParentID != "" {
			children[entry.ParentID] = append(children[entry.ParentID], entry.ID)
		}
	}
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, children[id]...)
	}
	return seen
}

// matchNativeEvents pairs each agent-derived event with the next native entry
// of the same class within the request subtree.
func matchNativeEvents(entries []NativeEntry, inRequest map[string]bool, events []store.Event) []store.EventNativeLink {
	links := make([]store.EventNativeLink, 0, len(events))
	used := make([]bool, len(entries))
	next := 0
	for _, event := range events {
		class := eventClass(event)
		if class == "" {
			continue
		}
		for index := next; index < len(entries); index++ {
			if used[index] || !inRequest[entries[index].ID] {
				continue
			}
			if nativeClass(entries[index]) != class {
				continue
			}
			used[index] = true
			links = append(links, store.EventNativeLink{EventID: event.ID, NativeEntryID: entries[index].ID})
			next = index + 1
			break
		}
	}
	return links
}

func eventClass(event store.Event) string {
	switch event.Kind {
	case session.KindMessage:
		role := ""
		if value, ok := event.Payload.(map[string]any); ok {
			role, _ = value["role"].(string)
		}
		return "message:" + strings.ToLower(role)
	case session.KindToolCall:
		return "tool"
	default:
		return ""
	}
}

func nativeClass(entry NativeEntry) string {
	switch strings.ToLower(entry.Role) {
	case "assistant":
		return "message:assistant"
	case "user":
		return "message:user"
	case "system":
		return "message:system"
	case "toolresult", "tool_result":
		return "tool"
	default:
		return ""
	}
}

// reconcileNative makes the harness session authoritative for usage, cost, and
// the resolved report text. It is a no-op when the session has no native
// record, so scripted adapters keep their reported result.
func reconcileNative(ctx context.Context, source nativeSource, requestID string, result *Result) {
	if requestID != "" {
		if report, ok, err := source.Report(ctx, requestID); err == nil && ok && strings.TrimSpace(report.Text) != "" {
			result.Text = report.Text
			result.ReportText = report.Text
			result.ReportEntryID = report.EntryID
		}
	}
	stats, err := source.Stats(ctx)
	if err != nil {
		return
	}
	result.LeafEntryID = stats.LeafID
	result.Usage = stats.Usage
	if stats.ContextTokens > 0 {
		result.ContextTokens = stats.ContextTokens
	}
	if stats.ContextWindow > 0 {
		result.ContextWindow = stats.ContextWindow
	}
}

// invoke runs an open session with a sink that cancels the invocation if the
// sink cannot persist a required live event.
func invoke(ctx context.Context, native Session, prompt Prompt, sink EventSink) (Result, error) {
	invocationCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	var sinkErr error
	requiredSink := func(eventCtx context.Context, event Event) error {
		if sink == nil {
			return nil
		}
		err := sink(eventCtx, event)
		if err == nil {
			return nil
		}
		mu.Lock()
		if sinkErr == nil {
			sinkErr = err
		}
		mu.Unlock()
		cancel()
		return err
	}

	result, runErr := native.Prompt(invocationCtx, prompt, requiredSink)
	mu.Lock()
	persistenceErr := sinkErr
	mu.Unlock()
	if persistenceErr != nil {
		return result, errors.Join(persistenceErr, runErr)
	}
	if runErr == nil {
		runErr = invocationCtx.Err()
	}
	return result, runErr
}

func fingerprint(repoPath string) (string, error) {
	if repoPath == "" {
		return "", nil
	}
	return factorygit.Fingerprint(repoPath)
}

func persistedUsage(value Usage) session.Usage {
	return session.Usage{Input: value.Input, Output: value.Output, CacheRead: value.CacheRead, CacheWrite: value.CacheWrite, Reasoning: value.Reasoning, TotalTokens: value.TotalTokens}
}
