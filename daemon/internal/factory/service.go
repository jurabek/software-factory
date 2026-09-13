package factory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	"github.com/jurabek/software-factory/daemon/internal/config"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
)

const maxCapturedOutput = 64 << 10

var (
	ErrStalePlan       = errors.New("plan digest is stale")
	ErrInvalidFeedback = errors.New("feedback is required")
)

type Dependencies struct {
	Store       *store.DB
	Config      config.Config
	ConfigPath  string
	Harnesses   harness.Registry
	Git         factorygit.Runner
	Sandbox     Sandbox
	NewPipeline func(*Service) Pipeliner
}

// Pipeliner is the synchronous workflow capability consumed by Factory.
// Pipeline owns stage order, the approval checkpoint, resume/retry position,
// and explicit result handoffs; Factory owns task creation, controls, worker
// ownership, cancellation, and shutdown.
type Pipeliner interface {
	Run(context.Context, string) (pipeline.Result, error)
}

type Service struct {
	root       string
	db         *store.DB
	config     config.Config
	configPath string
	harnesses  harness.Registry
	git        factorygit.Runner
	sandbox    Sandbox
	mu         sync.Mutex
	cancel     map[string]*execution
	taskLocks  sync.Map
	tasks      *taskService
	pipelines  *pipelineService
	snapshots  *workspace.Service
	quality    *qualityService
	pipeliner  Pipeliner
}

type taskStore interface {
	CreateActiveTask(context.Context, store.Task) error
	DeleteTask(context.Context, string) error
	Task(context.Context, string) (store.Task, error)
	TaskSessions(context.Context, string) ([]store.Task, error)
}

type taskService struct {
	root      string
	db        taskStore
	config    config.Config
	harnesses harness.Registry
	git       factorygit.Runner
	sandbox   Sandbox
	pipelines *pipelineService
}

type pipelineStore interface {
	Phases(context.Context, string) ([]store.Phase, error)
}

type pipelineService struct {
	db         pipelineStore
	config     config.Config
	configPath string
}

type qualityStore interface {
	Checks(context.Context, string) ([]store.Check, error)
	Comparisons(context.Context, string) ([]store.Comparison, error)
	EndProcess(context.Context, string, int, int) error
	Phases(context.Context, string) ([]store.Phase, error)
	SaveCheck(context.Context, store.Check) error
	SaveComparison(context.Context, store.Comparison) error
	SaveTestChanges(context.Context, []store.TestChange) error
	StartProcess(context.Context, string, string, string, string, int, string) (int64, error)
}

type qualityService struct {
	db        qualityStore
	git       factorygit.Runner
	snapshots *workspace.Service
}

type execution struct {
	cancel context.CancelFunc
	done   chan struct{}
	next   func(context.Context, string) error
}

type Repository struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Repo string `json:"repo,omitempty"`
}
type CreateRequest struct {
	Request     string     `json:"request"`
	Repository  Repository `json:"repository"`
	Pipeline    string     `json:"pipeline,omitempty"`
	CodingAgent string     `json:"coding_agent,omitempty"`
	Model       string     `json:"model,omitempty"`
	Thinking    string     `json:"thinking,omitempty"`
}
type CreateSessionRequest struct {
	Request string `json:"request"`
}
type Diff struct {
	Files []string `json:"files"`
	Patch string   `json:"patch"`
}

func NewService(root string, dependencies Dependencies) *Service {
	pipelines := &pipelineService{db: dependencies.Store, config: dependencies.Config, configPath: dependencies.ConfigPath}
	snapshots := workspace.New(dependencies.Store, dependencies.Git)
	service := &Service{
		root:       root,
		db:         dependencies.Store,
		config:     dependencies.Config,
		configPath: dependencies.ConfigPath,
		harnesses:  dependencies.Harnesses,
		git:        dependencies.Git,
		sandbox:    dependencies.Sandbox,
		cancel:     map[string]*execution{},
		tasks: &taskService{
			root: root, db: dependencies.Store, config: dependencies.Config,
			harnesses: dependencies.Harnesses, git: dependencies.Git, sandbox: dependencies.Sandbox, pipelines: pipelines,
		},
		pipelines: pipelines,
		snapshots: snapshots,
		quality:   &qualityService{db: dependencies.Store, git: dependencies.Git, snapshots: snapshots},
	}
	if dependencies.NewPipeline != nil {
		service.pipeliner = dependencies.NewPipeline(service)
	}
	return service
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (store.Task, error) {
	task, err := s.tasks.create(ctx, request, "")
	return s.launchCreatedTask(ctx, task, err)
}

func (s *Service) CreateSession(ctx context.Context, taskID string, request CreateSessionRequest) (store.Task, error) {
	task, err := s.tasks.CreateSession(ctx, taskID, request)
	return s.launchCreatedTask(ctx, task, err)
}

func (s *Service) launchCreatedTask(ctx context.Context, task store.Task, err error) (store.Task, error) {
	if err != nil {
		return store.Task{}, err
	}
	if err = s.ensureBranch(ctx, task.ID, ""); err != nil {
		return store.Task{}, err
	}
	created, err := s.db.Task(ctx, task.ID)
	if err != nil {
		return store.Task{}, err
	}
	s.launch(task.ID, s.progress)
	return created, nil
}

func (s *taskService) CreateSession(ctx context.Context, taskID string, request CreateSessionRequest) (store.Task, error) {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.ParentTaskID != "" {
		task, err = s.db.Task(ctx, task.ParentTaskID)
		if err != nil {
			return store.Task{}, err
		}
	}
	repository := Repository{Type: task.RepositoryType}
	if task.RepositoryType == "local" {
		repository.Path = task.RepositorySource
	} else {
		repository.Repo = task.RepositorySource
	}
	return s.create(ctx, CreateRequest{
		Request:     request.Request,
		Repository:  repository,
		CodingAgent: task.CodingAgent,
		Model:       task.Model,
		Thinking:    task.Thinking,
		Pipeline:    task.Pipeline,
	}, task.ID)
}

func (s *taskService) create(ctx context.Context, request CreateRequest, parentTaskID string) (store.Task, error) {
	request.Request = strings.TrimSpace(request.Request)
	if request.Request == "" {
		return store.Task{}, fmt.Errorf("task description is required")
	}
	request.CodingAgent = strings.TrimSpace(request.CodingAgent)
	request.Model = strings.TrimSpace(request.Model)
	request.Thinking = strings.TrimSpace(request.Thinking)
	request.Pipeline = strings.TrimSpace(request.Pipeline)
	configured, selectedPipeline, err := s.pipelines.selectPipeline(request.Pipeline)
	if err != nil {
		return store.Task{}, err
	}
	if request.CodingAgent != "" && !config.IsValidHarness(request.CodingAgent) {
		return store.Task{}, fmt.Errorf("coding_agent must be pi or codex")
	}
	if request.Thinking != "" {
		harnessForThinking := request.CodingAgent
		if harnessForThinking == "" {
			harnessForThinking = s.config.Defaults.CodingAgent
		}
		if harnessForThinking == "" {
			harnessForThinking = "pi"
		}
		if !config.IsValidThinkingFor(harnessForThinking, request.Thinking) {
			return store.Task{}, fmt.Errorf("thinking %q unsupported for %s", request.Thinking, harnessForThinking)
		}
	}
	if request.CodingAgent != "" {
		if _, ok := s.harnesses.Get(request.CodingAgent); !ok && s.harnesses != nil {
			return store.Task{}, fmt.Errorf("harness %s unavailable", request.CodingAgent)
		}
	}
	id, err := taskID()
	if err != nil {
		return store.Task{}, err
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	source, submitted, err := normalizeRepository(request.Repository)
	if err != nil {
		return store.Task{}, err
	}
	workspace := s.taskDir(id)
	for _, directory := range []string{workspace, filepath.Join(workspace, "workspace", "repository"), filepath.Join(workspace, "attempts"), filepath.Join(workspace, "snapshots"), filepath.Join(workspace, "artifacts"), filepath.Join(workspace, "sessions"), filepath.Join(workspace, "workspace", "snapshots"), filepath.Join(workspace, "workspace", "branches"), filepath.Join(workspace, "workspace", "attempts")} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			_ = os.RemoveAll(workspace)
			return store.Task{}, fmt.Errorf("create task workspace: %w", err)
		}
	}
	configured = config.ApplyTaskOverrides(configured, request.CodingAgent, request.Model, request.Thinking)
	configSnapshot, err := snapshotConfig(configured)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("encode task config: %w", err)
	}
	if len(configured.Agents) == 0 {
		configSnapshot = ""
	}
	task := store.Task{ID: id, ParentTaskID: parentTaskID, Request: request.Request, WorkspacePath: workspace, RepositoryType: request.Repository.Type, RepositorySource: source, SubmittedRepositoryPath: submitted, State: string(Preparing), Pipeline: selectedPipeline.Name, ConfigSnapshot: configSnapshot, CreatedAt: createdAt, StartedAt: createdAt, CodingAgent: request.CodingAgent, Model: request.Model, Thinking: request.Thinking}
	metadata, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("encode task metadata: %w", err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "task.json"), metadata, 0o600); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("write task metadata: %w", err)
	}
	if err := s.db.CreateActiveTask(ctx, task); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, err
	}
	return task, nil
}

func (s *Service) ensureBranch(ctx context.Context, taskID, parent string) error {
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return err
	}
	if task.SelectedBranchID != "" {
		return nil
	}
	branches, err := s.db.Branches(ctx, taskID)
	if err != nil {
		return err
	}
	if len(branches) > 0 {
		return s.db.SelectBranch(ctx, taskID, branches[0].ID)
	}
	branch := store.Branch{ID: randomID(), TaskID: taskID, ParentBranchID: parent, Status: "active", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = s.db.CreateBranch(ctx, branch); err != nil {
		return err
	}
	return s.db.SelectBranch(ctx, taskID, branch.ID)
}

func (s *Service) Approve(ctx context.Context, id, actor, expectedDigest string) error {
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest == "" {
		return fmt.Errorf("plan_digest is required")
	}
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State != string(AwaitingApproval) {
		return store.ErrConflict
	}
	payload, err := s.db.ValidEnvelope(ctx, id, "planner")
	if err != nil {
		return err
	}
	plan, err := ValidatePlan(payload)
	if err != nil || len(plan.Questions) > 0 {
		return store.ErrConflict
	}
	reportDigest := ""
	artifacts, artifactErr := s.db.Artifacts(ctx, id)
	if artifactErr != nil {
		return artifactErr
	}
	for _, artifact := range artifacts {
		if artifact.AttemptID != "" && artifact.Type == "plan_report" {
			reportDigest = artifact.Digest
		}
	}
	currentDigest := planApprovalDigest(payload, reportDigest)
	if expectedDigest != currentDigest {
		return ErrStalePlan
	}
	event := store.Event{
		ID: randomID(), TaskID: id, Kind: session.KindCustom, Name: "task_approved",
		Payload: session.CustomPayload{CustomType: "task_approved", Data: session.BoundedJSON(map[string]any{
			"task_id": id, "plan_digest": currentDigest, "actor": actor,
		})},
		Display:          session.Display{Role: "system", Status: "success", Title: "Plan approved"},
		AvailableActions: []string{"pause", "abort"}, StartedAt: time.Now().UTC(),
	}
	if err := s.db.ApproveWithEvent(ctx, s.taskDir(id), id, currentDigest, actor, event); err != nil {
		return err
	}
	s.launch(id, s.progress)
	return nil
}

func (s *Service) Pause(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	state := State(task.State)
	if !CanTransition(state, Paused) {
		return store.ErrConflict
	}
	if err := s.stopAndWait(ctx, id); err != nil {
		return err
	}
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	task, err = s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !CanTransition(State(task.State), Paused) {
		return store.ErrConflict
	}
	return s.db.Transition(ctx, id, task.State, string(Paused), task.ActivePhase, "")
}

func (s *Service) Abort(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !CanTransition(State(task.State), Aborted) {
		return store.ErrConflict
	}
	if err := s.stopAndWait(ctx, id); err != nil {
		return err
	}
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	var messages []store.Message
	task, err = s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !CanTransition(State(task.State), Aborted) {
		return store.ErrConflict
	}
	messages, err = s.db.AbortTask(ctx, id, task.State, task.ActivePhase)
	if err != nil {
		return err
	}
	for _, message := range messages {
		_ = s.traceMessage(ctx, message, nil)
	}
	return nil
}

func (s *Service) Resume(ctx context.Context, id string) error {
	lock := s.taskLock(id)
	lock.Lock()
	defer lock.Unlock()
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State != string(Paused) && task.State != string(Blocked) {
		return store.ErrConflict
	}
	if message, messageErr := s.db.NextQueuedTaskMessage(ctx, id); messageErr == nil {
		target := stateForRole(message.RecipientRole)
		if !CanTransition(State(task.State), target) {
			return store.ErrConflict
		}
		if err := s.db.Transition(ctx, id, task.State, string(target), task.ActivePhase, ""); err != nil {
			return err
		}
		s.launch(id, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, message.StageID) })
		return nil
	} else if !errors.Is(messageErr, store.ErrNotFound) {
		return messageErr
	}
	if task.State == string(Blocked) && task.Error == "unresolved_questions" {
		return store.ErrConflict
	}
	_, pipeline, pipelineErr := s.pipelines.taskPipeline(task)
	if pipelineErr != nil {
		return pipelineErr
	}
	stage, _, stageOK := stageDefinition(pipeline, task.ActiveStage)
	if !stageOK {
		return store.ErrConflict
	}
	target := stageState(stage.Kind)
	if !CanTransition(State(task.State), target) {
		return store.ErrConflict
	}
	if err := s.db.Transition(ctx, id, task.State, string(target), task.ActivePhase, ""); err != nil {
		return err
	}
	s.launch(id, s.progress)
	return nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.tasks.Delete(ctx, id)
}

func (s *taskService) Delete(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	tasks := []store.Task{task}
	if task.ParentTaskID == "" {
		tasks, err = s.db.TaskSessions(ctx, id)
		if err != nil {
			return err
		}
	}
	for _, session := range tasks {
		if isActive(State(session.State)) {
			return store.ErrConflict
		}
	}
	for _, session := range tasks {
		if s.sandbox != nil {
			_ = s.sandbox.Cleanup(ctx, CleanupRequest{TaskID: session.ID, WorkspaceRoot: session.WorkspacePath, SourceType: session.RepositoryType, CanonicalPath: session.CanonicalRepositoryPath, WorkingPath: session.RepositoryPath})
		}
		if err := os.RemoveAll(s.taskDir(session.ID)); err != nil {
			return fmt.Errorf("remove task files: %w", err)
		}
	}
	return s.db.DeleteTask(ctx, id)
}

func (s *Service) Diff(ctx context.Context, id string) (Diff, error) {
	return s.tasks.Diff(ctx, id)
}

func (s *taskService) Diff(ctx context.Context, id string) (Diff, error) {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return Diff{}, err
	}
	return s.diffRepository(ctx, task, false)
}

func (s *taskService) diffRepository(ctx context.Context, task store.Task, reviewBase bool) (Diff, error) {
	base := task.BaseSHA
	if reviewBase && task.ReviewBaseSHA != "" {
		base = task.ReviewBaseSHA
	}
	files, err := factorygit.ChangedFiles(ctx, s.git, task.RepositoryPath, base)
	if err != nil {
		return Diff{}, err
	}
	patch, err := factorygit.Diff(ctx, s.git, task.RepositoryPath, base)
	if err != nil {
		return Diff{}, err
	}
	return Diff{Files: files, Patch: patch}, nil
}

func (s *taskService) taskDir(id string) string { return filepath.Join(s.root, "tasks", id) }

func (s *Service) launch(id string, run func(context.Context, string) error) {
	s.mu.Lock()
	if active := s.cancel[id]; active != nil {
		if active.next == nil {
			active.next = run
		}
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &execution{cancel: cancel, done: make(chan struct{})}
	s.cancel[id] = active
	s.mu.Unlock()
	s.runExecution(ctx, id, active, run)
}

func (s *Service) runExecution(ctx context.Context, id string, active *execution, run func(context.Context, string) error) {
	go func() {
		var runErr error
		defer func() {
			var next func(context.Context, string) error
			var successor *execution
			var successorCtx context.Context
			s.mu.Lock()
			if s.cancel[id] == active {
				next = active.next
				if next == nil {
					delete(s.cancel, id)
				} else {
					var cancel context.CancelFunc
					successorCtx, cancel = context.WithCancel(context.Background())
					successor = &execution{cancel: cancel, done: make(chan struct{})}
					s.cancel[id] = successor
				}
			}
			s.mu.Unlock()
			close(active.done)
			if successor != nil {
				s.runExecution(successorCtx, id, successor, next)
			} else if !errors.Is(runErr, context.Canceled) {
				s.kickQueuedMessage(id)
			}
		}()
		runErr = run(ctx, id)
		if runErr == nil && ctx.Err() != nil {
			runErr = ctx.Err()
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			task, getErr := s.db.Task(context.Background(), id)
			if getErr == nil && task.State != string(Paused) && task.State != string(Aborted) && task.State != string(Blocked) {
				_ = s.db.Transition(context.Background(), id, task.State, string(Blocked), task.ActivePhase, runErr.Error())
			}
		}
	}()
}

func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
	workers := make(map[string]*execution, len(s.cancel))
	for id, worker := range s.cancel {
		workers[id] = worker
		worker.next = nil
		worker.cancel()
	}
	s.mu.Unlock()
	for id, worker := range workers {
		select {
		case <-worker.done:
		case <-ctx.Done():
			return
		}
		task, err := s.db.Task(ctx, id)
		if err == nil && isActive(State(task.State)) {
			_ = s.db.Transition(ctx, id, task.State, string(Blocked), task.ActivePhase, "server shutting down")
		}
	}
}

func (s *Service) stopAndWait(ctx context.Context, id string) error {
	for {
		s.mu.Lock()
		active := s.cancel[id]
		if active != nil {
			active.next = nil
			active.cancel()
		}
		s.mu.Unlock()
		if active == nil {
			return nil
		}
		select {
		case <-active.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) plan(ctx context.Context, task store.Task, revision map[string]any) error {
	before, err := repositoryFingerprint(ctx, s.git, task)
	if err != nil {
		return err
	}
	phase, err := s.beginPhase(ctx, task.ID, "planning", "agent", "planner", "Create implementation plan")
	if err != nil {
		return err
	}
	data := map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.RepositoryPath, "Workspace": task.WorkspacePath}
	for key, value := range revision {
		data[key] = value
	}
	validate := validatorForRole("planner")
	payload, err := s.runRole(ctx, task, phase, "planner", data, validate)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	_, err = s.completeAgentPhase(ctx, task, phase, "planner", "planner", validate, payload, func(payload string) error {
		after, changedErr := repositoryFingerprint(ctx, s.git, task)
		if changedErr != nil {
			return changedErr
		}
		if before != after {
			return fmt.Errorf("planner modified repository")
		}
		return nil
	}, AwaitingApproval)
	return err
}

type validator func(string) (any, error)

func (s *Service) runRole(ctx context.Context, task store.Task, phase store.Phase, role string, data map[string]any, validate validator) (string, error) {
	taskConfig, err := s.taskConfig(task)
	if err != nil {
		return "", err
	}
	stageID := role
	agentName := role
	if phase.Kind != "agent" && phase.Owner != "" {
		stageID = phase.Name
		agentName = phase.Owner
	}
	agent, ok := agentForRole(taskConfig, agentName)
	if !ok {
		return "", fmt.Errorf("agent %s not configured", agentName)
	}
	systemPrompt, userPrompt, err := s.renderPrompts(agent, data)
	if err != nil {
		return "", err
	}
	harnessName := taskConfig.Defaults.CodingAgent
	return agentexec.RunTurn(ctx, agentexec.Deps{
		DB: s.db, Harnesses: s.harnesses, Git: s.git,
		AgentDeadlineMS: taskConfig.Runtime.AgentDeadlineMS,
		JSONFixAttempts: taskConfig.Runtime.JSONFixAttempts,
	}, agentexec.TurnInput{
		TaskID: task.ID, Phase: phase, Role: role,
		HarnessName: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color,
		RepoPath: task.RepositoryPath, SessionDir: filepath.Join(s.taskDir(task.ID), "sessions", stageID, harnessName),
		SystemPrompt: systemPrompt, UserPrompt: userPrompt,
		ReadOnly: phaseReadOnly(phase, role), EnvelopeKind: phaseEnvelopeKind(phase, role),
		CorrectionSuffix: envelopeInstructions(phaseEnvelopeKind(phase, role)),
		Validate:         agentexec.Validate(validate),
		Sink:             s.eventSink(task.ID, phase.ID, harnessName),
	})
}

func reportMarkdown(value any) (string, error) {
	switch envelope := value.(type) {
	case Plan:
		return envelope.Report, nil
	case Build:
		return envelope.Report, nil
	case Review:
		return envelope.Report, nil
	default:
		body, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode validated envelope: %w", err)
		}
		var fields struct {
			Report string `json:"report_markdown"`
		}
		if err := json.Unmarshal(body, &fields); err != nil || strings.TrimSpace(fields.Report) == "" {
			return "", fmt.Errorf("validated envelope has no report_markdown")
		}
		return fields.Report, nil
	}
}

func planApprovalDigest(payload, reportDigest string) string {
	digest := sha256.Sum256([]byte(payload + "\n" + reportDigest))
	return hex.EncodeToString(digest[:])
}

func (s *Service) publishReport(ctx context.Context, task store.Task, phase store.Phase, kind, content, producer string) error {
	artifact := s.reportArtifact(task, phase, kind, content, producer)
	return s.db.CreateArtifact(ctx, artifact)
}

func (s *Service) reportArtifact(task store.Task, phase store.Phase, kind, content, producer string) store.Artifact {
	if kind == "planner" {
		kind = "plan"
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
	provenance, _ := json.Marshal(map[string]string{
		"task_id": task.ID, "stage_id": phase.Name, "attempt_id": phase.ID, "producer": producer,
	})
	return store.Artifact{
		ID: randomID(), TaskID: task.ID, AttemptID: phase.ID, Type: kind + "_report", Digest: digest,
		Content: content, MediaType: "text/markdown", Producer: producer, Provenance: string(provenance),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func persistedUsage(value harness.Usage) session.Usage {
	return session.Usage{Input: value.Input, Output: value.Output, CacheRead: value.CacheRead, CacheWrite: value.CacheWrite, Reasoning: value.Reasoning, TotalTokens: value.TotalTokens}
}

func (s *Service) renderPrompts(agent config.Agent, data map[string]any) (string, string, error) {
	return agentexec.RenderPrompts(
		agent.Name,
		agent.PromptEngineering.SystemContent, agent.PromptEngineering.System,
		agent.PromptEngineering.UserContent, agent.PromptEngineering.User,
		data, filepath.Dir(s.configPath),
		filepath.Join(s.taskDir(dataTask(data)), "prompts", agent.Name),
		envelopeInstructions(agent.Name),
	)
}

func phaseEnvelopeKind(phase store.Phase, role string) string {
	if phase.Kind == "build" || phase.Kind == "review" {
		return phase.Kind
	}
	return role
}

func phaseReadOnly(phase store.Phase, role string) bool {
	return phase.Kind == "review" || isReadOnlyOwner(role)
}
func dataTask(data map[string]any) string { return fmt.Sprint(data["TaskID"]) }

func (s *Service) beginPhase(ctx context.Context, taskID, name, kind, owner, description string) (store.Phase, error) {
	phases, err := s.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, err
	}
	_ = s.ensureBranch(ctx, taskID, "")
	task, _ := s.db.Task(ctx, taskID)
	definitionID := s.ensureDefinition(ctx, taskID, name, kind, owner)
	inputSnapshot := ""
	if isReadOnlyOwner(owner) && len(phases) > 0 {
		inputSnapshot = phases[len(phases)-1].OutputSnapshot
	}
	if inputSnapshot == "" {
		if snapshot, captureErr := s.CaptureSnapshot(ctx, store.Task{ID: taskID, WorkspacePath: s.taskDir(taskID)}); captureErr == nil {
			inputSnapshot = snapshot.Digest
		}
	}
	phase := store.Phase{ID: randomID(), TaskID: taskID, Sequence: len(phases) + 1, Name: name, Kind: kind, Owner: owner, Description: description, Status: "running", Attempt: 1, BranchID: task.SelectedBranchID, DefinitionID: definitionID, InputSnapshot: inputSnapshot}
	event := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: name, Owner: owner, Kind: kind, InputSnapshot: inputSnapshot})
	eventValue := store.Event{ID: randomID(), TaskID: taskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display, AvailableActions: AvailableActions(&phase, task.State), StartedAt: time.Now().UTC()}
	if err = s.db.StartPhaseWithEvent(ctx, s.taskDir(taskID), phase, task.State, eventValue); err != nil {
		return store.Phase{}, err
	}
	return phase, nil
}

func (s *Service) ensureDefinition(ctx context.Context, taskID, key, executor, owner string) string {
	existing, err := s.db.LatestDefinition(ctx, taskID, key)
	if err == nil {
		return existing.ID
	}
	definition := store.PhaseDefinition{ID: randomID(), TaskID: taskID, PhaseKey: key, Revision: 1, Executor: executor, Owner: owner, Spec: "{}"}
	definition.Digest = planDigest(key, 1, "{}")
	definition.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = s.db.CreateDefinition(ctx, definition); err != nil {
		return ""
	}
	return definition.ID
}

func (s *Service) endPhase(ctx context.Context, phase store.Phase, status string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	outputSnapshot := phase.InputSnapshot
	if status != "success" || !isReadOnlyOwner(phase.Owner) {
		if task, taskErr := s.db.Task(ctx, phase.TaskID); taskErr == nil {
			if snapshot, captureErr := s.CaptureSnapshot(ctx, task); captureErr == nil {
				if status == "success" && (phase.Kind == "agent" || phase.Kind == "build" || phase.Kind == "review" || phase.Kind == "check" || phase.Kind == "git") {
					outputSnapshot = snapshot.Digest
				} else if status != "success" {
					outputSnapshot = snapshot.Digest
				}
			}
		}
	}
	if outputSnapshot != "" && outputSnapshot != phase.InputSnapshot {
		_, _ = s.db.ExecContext(context.Background(), `update phases set output_snapshot=? where id=?`, outputSnapshot, phase.ID)
		phase.OutputSnapshot = outputSnapshot
	} else if phase.InputSnapshot != "" {
		_, _ = s.db.ExecContext(context.Background(), `update phases set output_snapshot=? where id=?`, phase.InputSnapshot, phase.ID)
		phase.OutputSnapshot = phase.InputSnapshot
	}
	event := session.NewPhaseEnd(session.PhasePayload{Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind, Status: status, Error: message, InputSnapshot: phase.InputSnapshot, OutputSnapshot: phase.OutputSnapshot})
	return s.db.EndPhaseWithEvent(ctx, s.taskDir(phase.TaskID), phase.ID, status, message, phase.OutputSnapshot, store.Event{ID: randomID(), TaskID: phase.TaskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display, StartedAt: time.Now().UTC()})
}

func (s *Service) completePhaseTransition(ctx context.Context, phase store.Phase, from, to State, status string, cause error) error {
	return s.completePhaseTransitionWithArtifact(ctx, phase, from, to, status, cause, nil)
}

func (s *Service) completePhaseTransitionWithArtifact(ctx context.Context, phase store.Phase, from, to State, status string, cause error, artifact *store.Artifact) error {
	return s.completeVerificationPhaseTransitionWithArtifact(ctx, phase, from, to, status, cause, nil, nil, artifact)
}

func (s *Service) completePlannerPhase(ctx context.Context, phase store.Phase, from, to State, status string, cause error, approval string, artifact *store.Artifact) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	task, err := s.db.Task(ctx, phase.TaskID)
	if err != nil {
		return err
	}
	event := session.NewPhaseEnd(session.PhasePayload{Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind, Status: status, Error: message, InputSnapshot: phase.InputSnapshot, OutputSnapshot: phase.OutputSnapshot})
	eventValue := store.Event{ID: randomID(), TaskID: phase.TaskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display, AvailableActions: AvailableActions(&phase, task.State), StartedAt: time.Now().UTC()}
	return s.db.CompletePlannerPhaseWithArtifactAndApproval(ctx, s.taskDir(phase.TaskID), phase.ID, phase.TaskID, string(from), string(to), status, message, approval, artifact, eventValue)
}

func (s *Service) completeVerificationPhaseTransition(ctx context.Context, phase store.Phase, from, to State, status string, cause error, checks []store.Check, comparisons []store.Comparison) error {
	return s.completeVerificationPhaseTransitionWithArtifact(ctx, phase, from, to, status, cause, checks, comparisons, nil)
}

func (s *Service) completeVerificationPhaseTransitionWithArtifact(ctx context.Context, phase store.Phase, from, to State, status string, cause error, checks []store.Check, comparisons []store.Comparison, artifact *store.Artifact) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	task, err := s.db.Task(ctx, phase.TaskID)
	if err != nil {
		return err
	}
	event := session.NewPhaseEnd(session.PhasePayload{
		Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind,
		Status: status, Error: message, InputSnapshot: phase.InputSnapshot,
		OutputSnapshot: phase.OutputSnapshot,
	})
	eventValue := store.Event{
		ID: randomID(), TaskID: phase.TaskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID,
		Kind: event.Kind, Name: event.Name, Payload: event.Payload, Display: event.Display,
		AvailableActions: AvailableActions(&phase, task.State), StartedAt: time.Now().UTC(),
	}
	if len(checks) > 0 || len(comparisons) > 0 {
		if artifact != nil {
			return s.db.CompleteVerificationPhaseWithEvidenceArtifactAndEvent(ctx, s.taskDir(phase.TaskID), phase.ID, phase.TaskID, string(from), string(to), status, message, checks, comparisons, artifact, eventValue)
		}
		return s.db.CompleteVerificationPhaseWithEvidenceAndEvent(ctx, s.taskDir(phase.TaskID), phase.ID, phase.TaskID, string(from), string(to), status, message, checks, comparisons, eventValue)
	}
	if artifact != nil {
		return s.db.CompletePhaseWithArtifactAndTransitionAndEvent(ctx, s.taskDir(phase.TaskID), phase.ID, phase.TaskID, string(from), string(to), status, message, artifact, eventValue)
	}
	return s.db.CompletePhaseWithTransitionAndEvent(ctx, s.taskDir(phase.TaskID), phase.ID, phase.TaskID, string(from), string(to), status, message, eventValue)
}

func (s *Service) traceBranch(ctx context.Context, taskID string, phase store.Phase, entry session.Entry) error {
	actions := AvailableActions(&phase, "")
	_, err := s.db.AppendEvent(ctx, s.taskDir(taskID), store.Event{ID: randomID(), TaskID: taskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display, AvailableActions: actions, StartedAt: time.Now().UTC()})
	return err
}

func (s *Service) failPhase(ctx context.Context, phase store.Phase, cause error) {
	_ = s.endPhase(context.Background(), phase, "failed", cause)
}

func (s *Service) eventSink(taskID, phaseID, harnessName string) harness.EventSink {
	return func(ctx context.Context, event harness.Event) error {
		switch payload := event.Payload.(type) {
		case session.ProcessStartPayload:
			if _, err := s.db.StartProcess(ctx, taskID, phaseID, harnessName, harnessName, payload.PID, payload.Command); err != nil {
				return err
			}
		case session.ProcessEndPayload:
			if err := s.db.EndProcess(ctx, taskID, payload.PID, payload.ExitCode); err != nil {
				return err
			}
		}
		return s.trace(ctx, taskID, phaseID, event)
	}
}

func (s *Service) trace(ctx context.Context, taskID, phaseID string, entry session.Entry) error {
	attemptID, branchID := phaseID, ""
	var actions []string
	if phaseID != "" {
		if phase, err := s.db.PhaseByID(ctx, taskID, phaseID); err == nil {
			attemptID = phase.ID
			branchID = phase.BranchID
			if task, taskErr := s.db.Task(ctx, taskID); taskErr == nil {
				actions = AvailableActions(&phase, task.State)
			} else {
				actions = AvailableActions(&phase, "")
			}
		}
	}
	_, err := s.db.AppendEvent(ctx, s.taskDir(taskID), store.Event{ID: randomID(), TaskID: taskID, PhaseID: phaseID, AttemptID: attemptID, BranchID: branchID, Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display, AvailableActions: actions, StartedAt: time.Now().UTC()})
	return err
}

func (s *Service) taskConfig(task store.Task) (config.Config, error) {
	return taskConfig(s.config, s.configPath, task)
}

func taskConfig(current config.Config, configPath string, task store.Task) (config.Config, error) {
	if task.ConfigSnapshot == "" {
		return current, nil
	}
	configured, problems, err := config.Parse([]byte(task.ConfigSnapshot), filepath.Dir(configPath))
	if err != nil {
		return config.Config{}, err
	}
	if len(problems) > 0 {
		return config.Config{}, fmt.Errorf("invalid task config: %s", strings.Join(problems, "; "))
	}
	return configured, nil
}

func validateTaskConfig(configured config.Config, harnesses harness.Registry) []string {
	var problems []string
	if !config.IsValidHarness(configured.Defaults.CodingAgent) {
		problems = append(problems, "defaults.coding_agent must be pi or codex")
	} else if harnesses != nil {
		if _, ok := harnesses.Get(configured.Defaults.CodingAgent); !ok {
			problems = append(problems, "harness "+configured.Defaults.CodingAgent+" unavailable")
		}
	}
	if !config.IsValidThinkingFor(configured.Defaults.CodingAgent, configured.Defaults.Thinking) {
		problems = append(problems, "defaults.thinking "+configured.Defaults.Thinking+" unsupported for "+configured.Defaults.CodingAgent)
	}
	candidates := map[string]bool{"planner": true}
	for _, pipeline := range configured.Pipelines {
		for _, stage := range pipeline.Stages {
			if stage.Agent != "" {
				candidates[stage.Agent] = true
			}
		}
	}
	for role := range candidates {
		found := false
		for _, a := range configured.Agents {
			if a.Name == role {
				found = true
				if !config.IsValidThinkingFor(configured.Defaults.CodingAgent, a.Thinking) {
					problems = append(problems, role+" thinking "+a.Thinking+" unsupported for "+configured.Defaults.CodingAgent)
				}
				if a.Model == "" {
					problems = append(problems, role+" model is required")
				}
			}
		}
		if !found {
			problems = append(problems, "missing agent: "+role)
		}
	}
	return problems
}

func agentForRole(configured config.Config, role string) (config.Agent, bool) {
	for _, agent := range configured.Agents {
		if agent.Name == role {
			return agent, true
		}
	}
	return config.Agent{}, false
}
func (s *Service) taskDir(id string) string { return filepath.Join(s.root, "tasks", id) }

func (s *Service) taskLock(id string) *sync.Mutex {
	value, _ := s.taskLocks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func normalizeRepository(input Repository) (source, submitted string, err error) {
	switch input.Type {
	case "local":
		if !filepath.IsAbs(input.Path) {
			return "", "", fmt.Errorf("local repository path must be absolute")
		}
		source, submitted = input.Path, input.Path
	case "github":
		if parts := strings.Split(input.Repo, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("github repository must be owner/repository")
		}
		source = input.Repo
	default:
		return "", "", fmt.Errorf("repository type must be local or github")
	}
	return source, submitted, nil
}

func taskID() (string, error) {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "SF-" + time.Now().UTC().Format("20060102") + "-" + hex.EncodeToString(bytes[:]), nil
}

func randomID() string {
	var bytes [12]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func readTaskProfile(task store.Task) (Materialization, error) {
	return workspace.ReadProfile(task)
}

func (s *Service) prepareRepository(ctx context.Context, task store.Task) (string, error) {
	if s.sandbox == nil {
		return "", fmt.Errorf("sandbox unavailable")
	}
	destination := filepath.Join(task.WorkspacePath, "workspace", "repository")
	profile, err := s.sandbox.Materialize(ctx, MaterializationRequest{TaskID: task.ID, SourceType: task.RepositoryType, Source: task.RepositorySource, Destination: destination})
	if err != nil {
		return "", err
	}
	if len(profile.Checks) == 0 {
		return "", fmt.Errorf("repository has no deterministic checks declared or detected")
	}
	encoded, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode repository profile: %w", err)
	}
	if err = os.WriteFile(filepath.Join(task.WorkspacePath, "repository-profile.json"), encoded, 0o600); err != nil {
		return "", fmt.Errorf("write repository profile: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `update tasks set canonical_repository_path=?,repository_path=?,base_sha=?,review_base_sha=?,branch_name=? where id=?`, profile.Root, destination, profile.BaseSHA, profile.BaseSHA, profile.BranchName, task.ID)
	if err != nil {
		return "", fmt.Errorf("save repository materialization: %w", err)
	}
	return destination, nil
}

func taskChangedFiles(ctx context.Context, runner factorygit.Runner, task store.Task, reviewBase bool) ([]string, error) {
	return workspace.ChangedFiles(ctx, runner, task, reviewBase)
}

func repositoryFingerprint(ctx context.Context, runner factorygit.Runner, task store.Task) (string, error) {
	return workspace.Fingerprint(ctx, runner, task)
}

func isReadOnlyOwner(owner string) bool {
	return owner == "planner" || owner == "reviewer"
}

func isActive(state State) bool {
	return state == Preparing || state == Planning || state == AwaitingApproval || state == Building || state == Checking || state == Reviewing
}

type tailCapture struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (capture *tailCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	length := len(data)
	capture.data = append(capture.data, data...)
	if len(capture.data) > capture.limit {
		capture.data = capture.data[len(capture.data)-capture.limit:]
	}
	return length, nil
}
func (capture *tailCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return string(capture.data)
}
