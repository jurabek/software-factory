package stagekit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
)

// RandomID returns a fresh opaque identifier.
func RandomID() string { return uuid.NewV7().String() }

func trimSpace(value string) string { return strings.TrimSpace(value) }

// PhaseByID loads one attempt.
func (k *Kit) PhaseByID(ctx context.Context, taskID, phaseID string) (store.Phase, error) {
	return k.db.PhaseByID(ctx, taskID, phaseID)
}

// BeginPhase starts a fresh attempt and emits its start event.
func (k *Kit) BeginPhase(ctx context.Context, taskID, name, kind, owner, description string) (store.Phase, error) {
	phases, err := k.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, err
	}
	task, _ := k.db.Task(ctx, taskID)
	definitionID := k.ensureDefinition(ctx, taskID, name, kind, owner)
	inputSnapshot := ""
	if IsReadOnlyOwner(owner) && len(phases) > 0 {
		inputSnapshot = phases[len(phases)-1].OutputSnapshot
	}
	if inputSnapshot == "" {
		if snapshot, captureErr := k.snapshots.CaptureSnapshot(ctx, store.Task{ID: taskID, WorkspacePath: k.TaskDir(taskID)}); captureErr == nil {
			inputSnapshot = snapshot.Digest
		}
	}
	phase := store.Phase{ID: RandomID(), TaskID: taskID, Sequence: len(phases) + 1, Name: name, Kind: kind, Owner: owner, Description: description, Status: "running", Attempt: 1, BranchID: task.SelectedBranchID, DefinitionID: definitionID, InputSnapshot: inputSnapshot}
	event := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: name, Owner: owner, Kind: kind, InputSnapshot: inputSnapshot})
	eventValue := store.Event{ID: RandomID(), TaskID: taskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display, AvailableActions: AvailableActions(&phase, task.State), StartedAt: time.Now().UTC()}
	if err = k.db.StartPhaseWithEvent(ctx, k.TaskDir(taskID), phase, task.State, eventValue); err != nil {
		return store.Phase{}, err
	}
	return phase, nil
}

// BeginOrReusePhase starts a stage phase, reusing a queued or running retry
// attempt for the same stage when one exists.
func (k *Kit) BeginOrReusePhase(ctx context.Context, taskID, name, kind, owner, description string) (store.Phase, error) {
	if phase, ok, err := k.pendingStagePhase(ctx, taskID, name); err != nil {
		return store.Phase{}, err
	} else if ok {
		if phase.Status == "queued" {
			if err = k.db.StartQueuedPhase(ctx, taskID, phase.ID); err != nil {
				return store.Phase{}, err
			}
			phase.Status = "running"
		}
		entry := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind, InputSnapshot: phase.InputSnapshot})
		if err = k.Trace(ctx, taskID, phase.ID, entry); err != nil {
			return store.Phase{}, err
		}
		return phase, nil
	}
	return k.BeginPhase(ctx, taskID, name, kind, owner, description)
}

func (k *Kit) pendingStagePhase(ctx context.Context, taskID, name string) (store.Phase, bool, error) {
	phases, err := k.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, false, err
	}
	for _, phase := range slices.Backward(phases) {

		if phase.Name != name || phase.Superseded {
			continue
		}
		if phase.Status == "queued" || phase.Status == "running" {
			return phase, true, nil
		}
		return store.Phase{}, false, nil
	}
	return store.Phase{}, false, nil
}

func (k *Kit) ensureDefinition(ctx context.Context, taskID, key, executor, owner string) string {
	existing, err := k.db.LatestDefinition(ctx, taskID, key)
	if err == nil {
		return existing.ID
	}
	definition := store.PhaseDefinition{ID: RandomID(), TaskID: taskID, PhaseKey: key, Revision: 1, Executor: executor, Owner: owner, Spec: "{}"}
	definition.Digest = PlanDigest(key, 1, "{}")
	definition.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = k.db.CreateDefinition(ctx, definition); err != nil {
		return ""
	}
	return definition.ID
}

// EndPhase finishes a phase without a transition, capturing its output snapshot.
func (k *Kit) EndPhase(ctx context.Context, phase store.Phase, status string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	outputSnapshot := phase.InputSnapshot
	if status != "success" || !IsReadOnlyOwner(phase.Owner) {
		if task, taskErr := k.db.Task(ctx, phase.TaskID); taskErr == nil {
			if snapshot, captureErr := k.snapshots.CaptureSnapshot(ctx, task); captureErr == nil {
				if status == "success" && (phase.Kind == "agent" || phase.Kind == "build" || phase.Kind == "review" || phase.Kind == "check" || phase.Kind == "git") {
					outputSnapshot = snapshot.Digest
				} else if status != "success" {
					outputSnapshot = snapshot.Digest
				}
			}
		}
	}
	if outputSnapshot != "" && outputSnapshot != phase.InputSnapshot {
		_, _ = k.db.ExecContext(context.Background(), `update phases set output_snapshot=? where id=?`, outputSnapshot, phase.ID)
		phase.OutputSnapshot = outputSnapshot
	} else if phase.InputSnapshot != "" {
		_, _ = k.db.ExecContext(context.Background(), `update phases set output_snapshot=? where id=?`, phase.InputSnapshot, phase.ID)
		phase.OutputSnapshot = phase.InputSnapshot
	}
	event := session.NewPhaseEnd(session.PhasePayload{Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind, Status: status, Error: message, InputSnapshot: phase.InputSnapshot, OutputSnapshot: phase.OutputSnapshot})
	return k.db.EndPhaseWithEvent(ctx, k.TaskDir(phase.TaskID), phase.ID, status, message, phase.OutputSnapshot, store.Event{ID: RandomID(), TaskID: phase.TaskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display, StartedAt: time.Now().UTC()})
}

// Fail marks a phase failed without a transition.
func (k *Kit) Fail(ctx context.Context, phase store.Phase, cause error) {
	_ = k.EndPhase(context.Background(), phase, "failed", cause)
}

// Completion describes an atomic phase completion and optional transition.
type Completion struct {
	Phase       store.Phase
	From, To    State
	Status      string
	Cause       error
	Artifact    *store.Artifact
	Approval    string
	Checks      []store.Check
	Comparisons []store.Comparison
	Planner     bool
}

// Complete atomically persists evidence, artifact, completion, and transition.
func (k *Kit) Complete(ctx context.Context, c Completion) error {
	message := ""
	if c.Cause != nil {
		message = c.Cause.Error()
	}
	task, err := k.db.Task(ctx, c.Phase.TaskID)
	if err != nil {
		return err
	}
	if c.Status == "success" {
		snapshot, captureErr := k.snapshots.CaptureSnapshot(ctx, task)
		if captureErr != nil {
			return fmt.Errorf("capture phase output snapshot: %w", captureErr)
		}
		c.Phase.OutputSnapshot = snapshot.Digest
	} else if c.Phase.OutputSnapshot == "" {
		c.Phase.OutputSnapshot = c.Phase.InputSnapshot
	}
	event := session.NewPhaseEnd(session.PhasePayload{
		Phase: c.Phase.ID, Name: c.Phase.Name, Owner: c.Phase.Owner, Kind: c.Phase.Kind,
		Status: c.Status, Error: message, InputSnapshot: c.Phase.InputSnapshot, OutputSnapshot: c.Phase.OutputSnapshot,
	})
	eventValue := store.Event{
		ID: RandomID(), TaskID: c.Phase.TaskID, PhaseID: c.Phase.ID, AttemptID: c.Phase.ID, BranchID: c.Phase.BranchID,
		Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display,
		AvailableActions: AvailableActions(&c.Phase, task.State), StartedAt: time.Now().UTC(),
	}
	dir := k.TaskDir(c.Phase.TaskID)
	if c.Planner {
		return k.db.CompletePlannerPhaseWithArtifactAndApproval(ctx, dir, c.Phase.ID, c.Phase.TaskID, string(c.From), string(c.To), c.Status, message, c.Phase.OutputSnapshot, c.Approval, c.Artifact, eventValue)
	}
	if len(c.Checks) > 0 || len(c.Comparisons) > 0 {
		if c.Artifact != nil {
			return k.db.CompleteVerificationPhaseWithEvidenceArtifactAndEvent(ctx, dir, c.Phase.ID, c.Phase.TaskID, string(c.From), string(c.To), c.Status, message, c.Phase.OutputSnapshot, c.Checks, c.Comparisons, c.Artifact, eventValue)
		}
		return k.db.CompleteVerificationPhaseWithEvidenceAndEvent(ctx, dir, c.Phase.ID, c.Phase.TaskID, string(c.From), string(c.To), c.Status, message, c.Phase.OutputSnapshot, c.Checks, c.Comparisons, eventValue)
	}
	if c.Artifact != nil {
		return k.db.CompletePhaseWithArtifactAndTransitionAndEvent(ctx, dir, c.Phase.ID, c.Phase.TaskID, string(c.From), string(c.To), c.Status, message, c.Phase.OutputSnapshot, c.Artifact, eventValue)
	}
	return k.db.CompletePhaseWithTransitionAndEvent(ctx, dir, c.Phase.ID, c.Phase.TaskID, string(c.From), string(c.To), c.Status, message, c.Phase.OutputSnapshot, eventValue)
}

// LatestStageAttempt returns the newest non-superseded attempt for a stage.
func (k *Kit) LatestStageAttempt(ctx context.Context, taskID, stageID string) (store.Phase, bool, error) {
	phases, err := k.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, false, err
	}
	for _, phase := range slices.Backward(phases) {
		if phase.Name == stageID && !phase.Superseded {
			return phase, true, nil
		}
	}
	return store.Phase{}, false, nil
}

// SuccessfulPhase returns the latest successful attempt for a stage.
func (k *Kit) SuccessfulPhase(ctx context.Context, taskID, name string) (store.Phase, bool, error) {
	phase, ok, err := k.LatestStageAttempt(ctx, taskID, name)
	if err != nil || !ok || phase.Status != "success" {
		return store.Phase{}, false, err
	}
	return phase, true, nil
}

// RequireAttempt verifies an attempt is still the successful one for a stage.
func (k *Kit) RequireAttempt(ctx context.Context, taskID, name, attemptID string) error {
	phase, ok, err := k.SuccessfulPhase(ctx, taskID, name)
	if err != nil {
		return err
	}
	if !ok || phase.ID != attemptID {
		return fmt.Errorf("%s result %s is no longer eligible", name, attemptID)
	}
	return nil
}

// AttemptAfter reports whether an attempt was produced after its upstream.
func (k *Kit) AttemptAfter(ctx context.Context, taskID, attemptID, upstreamID string) (bool, error) {
	attempt, err := k.db.PhaseByID(ctx, taskID, attemptID)
	if err != nil {
		return false, err
	}
	upstream, err := k.db.PhaseByID(ctx, taskID, upstreamID)
	if err != nil {
		return false, err
	}
	return EligibleAfter(attempt.Sequence, upstream.Sequence), nil
}

// PhaseEnvelope returns the latest valid envelope for a phase.
func (k *Kit) PhaseEnvelope(ctx context.Context, taskID, phaseID string) (string, error) {
	envelopes, err := k.db.Envelopes(ctx, taskID)
	if err != nil {
		return "", err
	}
	for _, envelope := range slices.Backward(envelopes) {
		if envelope.PhaseID == phaseID && envelope.Valid {
			return envelope.Payload, nil
		}
	}
	return "", store.ErrNotFound
}

// ReportArtifact builds a report artifact for a phase. nativeEntryID, when set,
// records the authoritative native entry the report was resolved from.
func (k *Kit) ReportArtifact(task store.Task, phase store.Phase, kind, content, producer, nativeEntryID string) store.Artifact {
	if kind == "planner" {
		kind = "plan"
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
	provenance := map[string]string{
		"task_id": task.ID, "stage_id": phase.Name, "attempt_id": phase.ID, "producer": producer,
	}
	if nativeEntryID != "" {
		provenance["native_entry_id"] = nativeEntryID
	}
	encoded, _ := json.Marshal(provenance)
	return store.Artifact{
		ID: RandomID(), TaskID: task.ID, AttemptID: phase.ID, Type: kind + "_report", Digest: digest,
		Content: content, MediaType: "text/markdown", Producer: producer, Provenance: string(encoded),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// AgentReportArtifact resolves an agent report from the turn's native assistant
// entry when present, validates it, extracts report_markdown, and builds an
// artifact. Small custom validators may fall back to the raw payload.
func (k *Kit) AgentReportArtifact(task store.Task, phase store.Phase, kind string, turn harness.TurnResult, validate func(string) (any, error)) (store.Artifact, error) {
	payload := turn.Payload
	source := payload
	if trimSpace(turn.ReportText) != "" {
		source = turn.ReportText
	}
	validated, err := validate(source)
	if err != nil {
		if trimSpace(payload) == "" {
			return store.Artifact{}, err
		}
		return k.ReportArtifact(task, phase, kind, source, kind, turn.ReportEntryID), nil
	}
	report, err := ReportMarkdown(validated)
	if err != nil {
		if trimSpace(payload) == "" {
			return store.Artifact{}, err
		}
		report = source
	}
	return k.ReportArtifact(task, phase, kind, report, kind, turn.ReportEntryID), nil
}

// ReportMarkdown extracts report_markdown from a validated envelope.
func ReportMarkdown(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode validated envelope: %w", err)
	}
	var fields struct {
		Report string `json:"report_markdown"`
	}
	if err := json.Unmarshal(body, &fields); err != nil || trimSpace(fields.Report) == "" {
		return "", fmt.Errorf("validated envelope has no report_markdown")
	}
	return fields.Report, nil
}

// PlanApprovalDigest binds a plan payload to its report digest.
func PlanApprovalDigest(payload, reportDigest string) string {
	digest := sha256.Sum256([]byte(payload + "\n" + reportDigest))
	return hex.EncodeToString(digest[:])
}

// EligibleAfter owns downstream lineage: a result is reusable only when it was
// produced after its upstream result.
func EligibleAfter(attemptSequence, upstreamSequence int) bool {
	return attemptSequence > upstreamSequence
}

// PlanDigest binds a phase key and revision to an amendment. Control-plane
// retry/revise operations reuse it so definition digests stay consistent.
func PlanDigest(key string, revision int, amendment string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", key, revision, amendment)))
	return fmt.Sprintf("%x", sum)
}
