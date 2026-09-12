package factory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"uuid"

	"github.com/jurabek/software-factory/daemon/internal/config"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"gopkg.in/yaml.v3"
)

const maxCapturedOutput = 64 << 10

var (
	ErrStalePlan       = errors.New("plan digest is stale")
	ErrInvalidFeedback = errors.New("feedback is required")
)

type Dependencies struct {
	Store      *store.DB
	Config     config.Config
	ConfigPath string
	Harnesses  harness.Registry
	Git        factorygit.Runner
	Sandbox    Sandbox
}

type runtime struct {
	root       string
	db         *store.DB
	config     config.Config
	configPath string
	harnesses  harness.Registry
	git        factorygit.Runner
	executions *executionOwner
}

type taskStore interface {
	CreateTask(context.Context, store.Task) error
	DeleteTask(context.Context, string) error
	Task(context.Context, string) (store.Task, error)
	TaskSessions(context.Context, string) ([]store.Task, error)
	Transition(context.Context, string, string, string, string, string) error
}

type taskService struct {
	root      string
	db        taskStore
	config    config.Config
	harnesses harness.Registry
	git       factorygit.Runner
	pipelines *pipelineService
	workspace workspaceLifecycle
}

type pipelineStore interface {
	Phases(context.Context, string) ([]store.Phase, error)
}

type pipelineService struct {
	db         pipelineStore
	config     config.Config
	configPath string
}

type snapshotStore interface {
	SaveSnapshot(context.Context, store.WorkspaceSnapshot) error
	Snapshot(context.Context, string) (store.WorkspaceSnapshot, error)
}

type snapshotService struct {
	db  snapshotStore
	git factorygit.Runner
}

type qualityStore interface {
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
	workspace workspaceLifecycle
}

type Service struct {
	*runtime
	tasks     *taskService
	pipelines *pipelineService
	workspace workspaceLifecycle
	quality   *qualityService
}

type Repository struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	Path    string `json:"path,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}
type CreateRequest struct {
	Request      string       `json:"request"`
	Repositories []Repository `json:"repositories"`
	Pipeline     string       `json:"pipeline,omitempty"`
	CodingAgent  string       `json:"coding_agent,omitempty"`
	Model        string       `json:"model,omitempty"`
	Thinking     string       `json:"thinking,omitempty"`
}
type CreateSessionRequest struct {
	Request string `json:"request"`
}
type Diff struct {
	Repositories []RepositoryDiff `json:"repositories"`
}
type RepositoryDiff struct {
	RepositoryID string   `json:"repository_id"`
	Name         string   `json:"name"`
	Files        []string `json:"files"`
	Patch        string   `json:"patch"`
}

func NewService(root string, dependencies Dependencies) *Service {
	runtime := &runtime{
		root:       root,
		db:         dependencies.Store,
		config:     dependencies.Config,
		configPath: dependencies.ConfigPath,
		harnesses:  dependencies.Harnesses,
		git:        dependencies.Git,
		executions: newExecutionOwner(),
	}
	pipelines := &pipelineService{db: dependencies.Store, config: dependencies.Config, configPath: dependencies.ConfigPath}
	snapshots := &snapshotService{db: dependencies.Store, git: dependencies.Git}
	workspace := &workspaceService{root: root, db: dependencies.Store, sandbox: dependencies.Sandbox, snapshots: snapshots, git: dependencies.Git}
	return &Service{
		runtime: runtime,
		tasks: &taskService{
			root: root, db: dependencies.Store, config: dependencies.Config,
			harnesses: dependencies.Harnesses, git: dependencies.Git, pipelines: pipelines, workspace: workspace,
		},
		pipelines: pipelines,
		workspace: workspace,
		quality:   &qualityService{db: dependencies.Store, git: dependencies.Git, workspace: workspace},
	}
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
	repositories := make([]Repository, 0, len(task.Repositories))
	for _, repository := range task.Repositories {
		input := Repository{Name: repository.Name, Type: repository.SourceType, Primary: repository.Primary}
		if repository.SourceType == "local" {
			input.Path = repository.SourceValue
		} else {
			input.Repo = repository.SourceValue
		}
		repositories = append(repositories, input)
	}
	return s.create(ctx, CreateRequest{
		Request:      request.Request,
		Repositories: repositories,
		CodingAgent:  task.CodingAgent,
		Model:        task.Model,
		Thinking:     task.Thinking,
		Pipeline:     task.Pipeline,
	}, task.ID)
}

func (s *taskService) create(ctx context.Context, request CreateRequest, parentTaskID string) (store.Task, error) {
	request.Request = strings.TrimSpace(request.Request)
	if request.Request == "" {
		return store.Task{}, fmt.Errorf("task description is required")
	}
	if len(request.Repositories) == 0 {
		return store.Task{}, fmt.Errorf("at least one repository is required")
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
	repositories, err := taskRepositories(id, request.Repositories, createdAt)
	if err != nil {
		return store.Task{}, err
	}
	workspace := s.taskDir(id)
	configured = config.ApplyTaskOverrides(configured, request.CodingAgent, request.Model, request.Thinking)
	configSnapshot, err := snapshotConfig(configured)
	if err != nil {
		return store.Task{}, fmt.Errorf("encode task config: %w", err)
	}
	if len(configured.Agents) == 0 {
		configSnapshot = ""
	}
	task := store.Task{ID: id, ParentTaskID: parentTaskID, Request: request.Request, WorkspacePath: workspace, Repositories: repositories, State: string(Preparing), Pipeline: selectedPipeline.Name, ConfigSnapshot: configSnapshot, CreatedAt: createdAt, StartedAt: createdAt, CodingAgent: request.CodingAgent, Model: request.Model, Thinking: request.Thinking}
	if err := s.db.CreateTask(ctx, task); err != nil {
		return store.Task{}, err
	}
	if err = s.workspace.Allocate(ctx, task); err != nil {
		_ = s.db.Transition(context.WithoutCancel(ctx), id, string(Preparing), string(Blocked), "", err.Error())
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
	expectedDigest = strings.TrimSpace(expectedDigest)
	if expectedDigest == "" {
		return fmt.Errorf("plan_digest is required")
	}
	return s.executions.withTask(id, func() error {
		return s.approve(ctx, id, actor, expectedDigest)
	})
}

func (s *Service) approve(ctx context.Context, id, actor, expectedDigest string) error {
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
	digest := sha256.Sum256([]byte(payload))
	currentDigest := hex.EncodeToString(digest[:])
	if expectedDigest != currentDigest {
		return ErrStalePlan
	}
	entry := session.NewPlanFeedback(session.PlanFeedbackPayload{Feedback: "approved", Actor: actor, PlanDigest: currentDigest})
	if err := s.db.CommitApproval(ctx, id, string(AwaitingApproval), currentDigest, actor, store.Event{
		ID: randomID(), TaskID: id, Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload,
		Display: entry.Display, StartedAt: time.Now().UTC(),
	}, s.taskDir(id)); err != nil {
		return err
	}
	s.launch(id, s.progress)
	return nil
}

func (s *Service) Pause(ctx context.Context, id string) error {
	if err := s.executions.stopAndWait(ctx, id); err != nil {
		return err
	}
	return s.executions.withTask(id, func() error { return s.pause(ctx, id) })
}

func (s *Service) pause(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	state := State(task.State)
	if !CanTransition(state, Paused) {
		return store.ErrConflict
	}
	return s.db.Transition(ctx, id, task.State, string(Paused), task.ActivePhase, "")
}

func (s *Service) Abort(ctx context.Context, id string) error {
	if err := s.executions.stopAndWait(ctx, id); err != nil {
		return err
	}
	return s.executions.withTask(id, func() error { return s.abort(ctx, id) })
}

func (s *Service) abort(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if !CanTransition(State(task.State), Aborted) {
		return store.ErrConflict
	}
	messages, err := s.db.AbortTask(ctx, id, task.State, task.ActivePhase)
	if err != nil {
		return err
	}
	for _, message := range messages {
		_ = s.traceMessage(ctx, message, nil)
	}
	return nil
}

func (s *Service) Resume(ctx context.Context, id string) error {
	return s.executions.withTask(id, func() error { return s.resume(ctx, id) })
}

func (s *Service) resume(ctx context.Context, id string) error {
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
		s.launch(id, func(ctx context.Context, id string) error { return s.continueMessages(ctx, id, message.RecipientRole) })
		return nil
	} else if !errors.Is(messageErr, store.ErrNotFound) {
		return messageErr
	}
	if task.State == string(Blocked) && task.Error == "unresolved_questions" {
		return store.ErrConflict
	}
	_, pipeline, pipelineErr := s.taskPipeline(task)
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
		if err := s.workspace.Cleanup(ctx, session); err != nil {
			return err
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
	return s.diffRepositories(ctx, task, false)
}

func (s *taskService) diffRepositories(ctx context.Context, task store.Task, reviewBase bool) (Diff, error) {
	result := Diff{Repositories: make([]RepositoryDiff, 0, len(task.Repositories))}
	for _, repository := range task.Repositories {
		if repository.WorkingPath == "" {
			continue
		}
		base := repository.BaseSHA
		if reviewBase && repository.ReviewBaseSHA != "" {
			base = repository.ReviewBaseSHA
		}
		files, diffErr := factorygit.ChangedFiles(ctx, s.git, repository.WorkingPath, base)
		if diffErr != nil {
			return Diff{}, diffErr
		}
		patch, diffErr := factorygit.Diff(ctx, s.git, repository.WorkingPath, base)
		if diffErr != nil {
			return Diff{}, diffErr
		}
		result.Repositories = append(result.Repositories, RepositoryDiff{RepositoryID: repository.ID, Name: repository.Name, Files: files, Patch: patch})
	}
	return result, nil
}

func (s *taskService) taskDir(id string) string { return filepath.Join(s.root, "tasks", id) }

func (s *Service) diffRepositories(ctx context.Context, task store.Task, reviewBase bool) (Diff, error) {
	return s.tasks.diffRepositories(ctx, task, reviewBase)
}

func (s *Service) launch(id string, run func(context.Context, string) error) {
	executionID := randomID()
	s.executions.start(id, executionID, func(ctx context.Context) error {
		runErr := run(ctx, id)
		if ctx.Err() != nil && !errors.Is(runErr, harness.ErrProcessTerminationUnconfirmed) {
			return ctx.Err()
		}
		return runErr
	}, func(ctx context.Context, runErr error) {
		if (ctx.Err() != nil || errors.Is(runErr, context.Canceled)) && !errors.Is(runErr, harness.ErrProcessTerminationUnconfirmed) {
			return
		}
		if runErr != nil {
			task, getErr := s.db.Task(context.Background(), id)
			if getErr == nil && task.State != string(Paused) && task.State != string(Aborted) && task.State != string(Blocked) {
				_ = s.db.Transition(context.Background(), id, task.State, string(Blocked), task.ActivePhase, runErr.Error())
			}
		}
	}, func(ctx context.Context, runErr error) {
		if ctx.Err() == nil && !errors.Is(runErr, context.Canceled) {
			s.kickQueuedMessage(id)
		}
	})
}

func (s *Service) Shutdown(ctx context.Context) {
	ids := s.executions.stopAll()
	for _, id := range ids {
		task, err := s.db.Task(context.Background(), id)
		if err == nil && isActive(State(task.State)) {
			_ = s.db.Transition(context.Background(), id, task.State, string(Blocked), task.ActivePhase, "server shutting down")
		}
	}
	if s.executions.wait(ctx) == nil {
		s.executions.resume()
	}
}

func (s *Service) prepareAndPlan(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State == string(Planning) && task.PrimaryRepositoryPath != "" {
		return s.plan(ctx, task, nil)
	}
	snapshot, err := os.ReadFile(s.configPath)
	if err != nil {
		return err
	}
	configured, problems, parseErr := config.Parse(snapshot, filepath.Dir(s.configPath))
	if parseErr != nil {
		return parseErr
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(problems, "; "))
	}
	configured = config.ApplyTaskOverrides(configured, task.CodingAgent, task.Model, task.Thinking)
	if problems := validateTaskConfig(configured, s.harnesses); len(problems) > 0 {
		return fmt.Errorf("invalid task config: %s", strings.Join(problems, "; "))
	}
	if task.CodingAgent != "" || task.Model != "" || task.Thinking != "" {
		overridden, marshalErr := yaml.Marshal(configured)
		if marshalErr != nil {
			return fmt.Errorf("encode task config: %w", marshalErr)
		}
		snapshot = overridden
	}
	phase, err := s.beginPhase(ctx, id, "prepare", "git", "factory", "Prepare repository")
	if err != nil {
		return err
	}
	primaryPath, err := s.prepareRepositories(ctx, task, phase)
	if err != nil {
		return err
	}
	profile, profileErr := os.ReadFile(filepath.Join(task.WorkspacePath, "repository-profiles", primaryRepositoryName(task)+".json"))
	if profileErr != nil {
		s.failPhase(ctx, phase, profileErr)
		return profileErr
	}
	if err = os.WriteFile(filepath.Join(task.WorkspacePath, "repository-profile.json"), profile, 0o600); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = os.WriteFile(filepath.Join(s.taskDir(id), "config-snapshot.yaml"), snapshot, 0o600); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.db.SetPrepared(ctx, id, primaryPath, string(snapshot)); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.endPhaseToState(ctx, phase, "success", nil, string(Preparing), string(Planning)); err != nil {
		return err
	}
	task, err = s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	return s.plan(ctx, task, nil)
}

func (s *Service) plan(ctx context.Context, task store.Task, revision map[string]any) error {
	before, err := repositoryFingerprints(ctx, s.git, task.Repositories)
	if err != nil {
		return err
	}
	phase, err := s.beginPhase(ctx, task.ID, "planning", "agent", "planner", "Create implementation plan")
	if err != nil {
		return err
	}
	data := map[string]any{"TaskID": task.ID, "Request": task.Request, "Repository": task.PrimaryRepositoryPath, "Repositories": task.Repositories, "Workspace": task.WorkspacePath}
	for key, value := range revision {
		data[key] = value
	}
	validate := validatorForRole("planner")
	payload, envelope, err := s.runRoleDeferredEnvelope(ctx, task, phase, "planner", data, validate)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	_, err = s.completeAgentPhaseWithEnvelope(ctx, task, phase, "planner", validate, payload, func(payload string) error {
		after, changedErr := repositoryFingerprints(ctx, s.git, task.Repositories)
		if changedErr != nil {
			return changedErr
		}
		if !sameFingerprints(before, after) {
			return fmt.Errorf("planner modified repository")
		}
		return nil
	}, AwaitingApproval, &envelope)
	return err
}

func (s *Service) buildCheckReview(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	profiles, err := s.workspace.InspectProfiles(ctx, task)
	if err != nil {
		return err
	}
	plan, err := s.db.ValidEnvelope(ctx, id, "planner")
	if err != nil {
		return err
	}
	if task.State == string(Building) {
		phase, beginErr := s.beginPhase(ctx, id, "building", "agent", "builder", "Implement approved plan")
		if beginErr != nil {
			return beginErr
		}
		validate := validatorForRole("builder")
		payload, runErr := s.runRole(ctx, task, phase, "builder", map[string]any{"TaskID": task.ID, "Request": task.Request, "Plan": plan}, validate)
		err = runErr
		if err != nil {
			s.failPhase(ctx, phase, err)
			return err
		}
		_, err = s.completeAgentPhase(ctx, task, phase, "builder", validate, payload, func(string) error {
			return s.validateBuilderPaths(ctx, task, profiles)
		}, Checking)
		if err != nil {
			return err
		}
	}
	task, _ = s.db.Task(ctx, id)
	if task.State == string(Checking) {
		phase, beginErr := s.beginPhase(ctx, id, "checks", "check", "factory", "Run deterministic checks")
		if beginErr != nil {
			return beginErr
		}
		for _, repository := range task.Repositories {
			if err = s.runChecks(ctx, task, phase, repository, profiles[repository.Name].Checks, "primary", ""); err != nil {
				s.failPhase(ctx, phase, err)
				return err
			}
		}
		if err = s.endPhaseToState(ctx, phase, "success", nil, string(Checking), string(Reviewing)); err != nil {
			return err
		}
	}
	task, _ = s.db.Task(ctx, id)
	before, err := repositoryFingerprints(ctx, s.git, task.Repositories)
	if err != nil {
		return err
	}
	changedFiles, err := taskChangedFiles(ctx, s.git, task.Repositories, true)
	if err != nil {
		return err
	}
	phase, err := s.beginPhase(ctx, id, "reviewing", "agent", "reviewer", "Review implementation")
	if err != nil {
		return err
	}
	checks, err := s.db.Checks(ctx, id)
	if err != nil {
		return err
	}
	testChanges, err := s.db.TestChanges(ctx, id)
	if err != nil {
		return err
	}
	comparisons, err := s.db.Comparisons(ctx, id)
	if err != nil {
		return err
	}
	changes, err := s.diffRepositories(ctx, task, true)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	validate := validatorForRole("reviewer")
	reviewPayload, err := s.runRole(ctx, task, phase, "reviewer", map[string]any{"TaskID": task.ID, "Request": task.Request, "Plan": plan, "Checks": checks, "TestChanges": testChanges, "Comparisons": comparisons, "ChangedFiles": changedFiles, "Diff": changes.Repositories}, validate)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	_, err = s.completeAgentPhase(ctx, task, phase, "reviewer", validate, reviewPayload, func(payload string) error {
		review, validateErr := ValidateReview(payload)
		if validateErr != nil {
			return validateErr
		}
		if !review.Approved {
			return fmt.Errorf("reviewer rejected implementation")
		}
		after, changedErr := repositoryFingerprints(ctx, s.git, task.Repositories)
		if changedErr != nil {
			return changedErr
		}
		if !sameFingerprints(before, after) {
			return fmt.Errorf("reviewer modified repository")
		}
		return nil
	}, Completed)
	return err
}

type validator func(string) (any, error)

func (s *Service) runRole(ctx context.Context, task store.Task, phase store.Phase, role string, data map[string]any, validate validator) (string, error) {
	payload, _, err := s.runRoleWithOptions(ctx, task, phase, role, data, validate, false)
	return payload, err
}

func (s *Service) runRoleDeferredEnvelope(ctx context.Context, task store.Task, phase store.Phase, role string, data map[string]any, validate validator) (string, store.Envelope, error) {
	payload, envelope, err := s.runRoleWithOptions(ctx, task, phase, role, data, validate, true)
	return payload, envelope, err
}

func (s *Service) runRoleWithOptions(ctx context.Context, task store.Task, phase store.Phase, role string, data map[string]any, validate validator, deferValid bool) (string, store.Envelope, error) {
	taskConfig, err := s.taskConfig(task)
	if err != nil {
		return "", store.Envelope{}, err
	}
	stageID := role
	agentName := role
	if phase.Kind != "agent" && phase.Owner != "" {
		stageID = phase.Name
		agentName = phase.Owner
	}
	agent, ok := agentForRole(taskConfig, agentName)
	if !ok {
		return "", store.Envelope{}, fmt.Errorf("agent %s not configured", agentName)
	}
	adapter, ok := s.harnesses.Get(taskConfig.Defaults.CodingAgent)
	if !ok {
		return "", store.Envelope{}, fmt.Errorf("harness %s unavailable", taskConfig.Defaults.CodingAgent)
	}
	systemPrompt, userPrompt, err := s.renderPrompts(agent, data)
	if err != nil {
		return "", store.Envelope{}, err
	}
	harnessName := taskConfig.Defaults.CodingAgent
	sessionDir := filepath.Join(s.taskDir(task.ID), "sessions", stageID, harnessName)
	storedSession, sessionErr := s.db.AgentSession(ctx, task.ID, stageID)
	if errors.Is(sessionErr, store.ErrNotFound) {
		storedSession, sessionErr = s.db.ReserveAgentSession(ctx, task.ID, store.AgentSession{StageID: stageID, AgentName: agentName, Role: agentName, Harness: harnessName, Model: agent.Model, Thinking: agent.Thinking, Color: agent.Color, HarnessSessionID: uuid.New().String(), SessionDirectory: sessionDir, AccountingComplete: true})
	}
	if sessionErr != nil {
		return "", store.Envelope{}, sessionErr
	}
	if storedSession.Harness != harnessName {
		return "", store.Envelope{}, fmt.Errorf("role %s session belongs to harness %s", role, storedSession.Harness)
	}
	additionalDirectories := make([]string, 0, len(task.Repositories))
	for _, repository := range task.Repositories {
		if !repository.Primary && repository.WorkingPath != "" {
			additionalDirectories = append(additionalDirectories, repository.WorkingPath)
		}
	}
	request := harness.Request{CWD: task.PrimaryRepositoryPath, Prompt: userPrompt, SystemPrompt: systemPrompt, Model: agent.Model, Thinking: agent.Thinking, SessionID: storedSession.HarnessSessionID, SessionDirectory: storedSession.SessionDirectory, RawOutputPath: filepath.Join(storedSession.SessionDirectory, "raw-output.jsonl"), DeadlineMS: taskConfig.Runtime.AgentDeadlineMS, Resume: storedSession.SessionReady, AdditionalDirectories: additionalDirectories}
	sessionReady := storedSession.SessionReady
	for attempt := 0; attempt <= taskConfig.Runtime.JSONFixAttempts; attempt++ {
		if attempt > 0 {
			request.Prompt = "Your previous final response was invalid: " + err.Error() + "\n" + envelopeInstructions(phaseEnvelopeKind(phase, role))
		}
		invocationID := uuid.New().String()
		if err := s.db.BeginAgentInvocation(ctx, task.ID, stageID, invocationID); err != nil {
			return "", store.Envelope{}, err
		}
		var before map[string]string
		if phaseReadOnly(phase, role) {
			before, err = repositoryFingerprints(ctx, s.git, task.Repositories)
			if err != nil {
				return "", store.Envelope{}, err
			}
		}
		result, runErr := adapter.Run(ctx, request, s.eventSink(task.ID, phase.ID, harnessName))
		if phaseReadOnly(phase, role) {
			after, fingerprintErr := repositoryFingerprints(ctx, s.git, task.Repositories)
			if fingerprintErr != nil {
				runErr = errors.Join(runErr, fingerprintErr)
			} else if !sameFingerprints(before, after) {
				runErr = errors.Join(runErr, fmt.Errorf("%s modified repository", role))
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
		finalizeErr := s.db.FinalizeAgentInvocation(cleanupCtx, task.ID, stageID, invocationID, store.AgentSession{StageID: stageID, AgentName: agentName, Role: agentName, Harness: harnessName, Provider: result.Provider, Model: result.Model, Thinking: agent.Thinking, Color: agent.Color, HarnessSessionID: storedSession.HarnessSessionID, SessionDirectory: storedSession.SessionDirectory, SessionReady: sessionReady, NativeTranscriptPath: result.NativeTranscriptPath, ContextTokens: result.ContextTokens, ContextWindow: result.ContextWindow, Usage: persistedUsage(result.Usage), Cost: result.Usage.Cost, AccountingComplete: result.AccountingComplete})
		cancel()
		if finalizeErr != nil {
			return "", store.Envelope{}, finalizeErr
		}
		request.Resume = sessionReady
		if runErr != nil {
			return "", store.Envelope{}, runErr
		}
		_, validationErr := validate(result.Text)
		valid := validationErr == nil
		tail := result.Text
		if len(tail) > maxCapturedOutput {
			tail = tail[len(tail)-maxCapturedOutput:]
		}
		stored := tail
		if valid {
			stored = result.Text
		}
		envelope := store.Envelope{ID: randomID(), TaskID: task.ID, PhaseID: phase.ID, StageID: stageID, AgentRole: stageID, OutputType: phaseEnvelopeKind(phase, role), Payload: stored, Valid: valid, Attempt: attempt + 1, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if valid && deferValid {
			return result.Text, envelope, nil
		}
		if err := s.db.SaveEnvelope(ctx, envelope.ID, envelope.TaskID, envelope.PhaseID, envelope.AgentRole, envelope.OutputType, envelope.Payload, envelope.Valid, envelope.Attempt); err != nil {
			return "", store.Envelope{}, err
		}
		if valid {
			return result.Text, store.Envelope{}, nil
		}
		err = validationErr
	}
	return "", store.Envelope{}, fmt.Errorf("%s envelope invalid after corrections: %w", role, err)
}

func persistedUsage(value harness.Usage) session.Usage {
	return session.Usage{Input: value.Input, Output: value.Output, CacheRead: value.CacheRead, CacheWrite: value.CacheWrite, Reasoning: value.Reasoning, TotalTokens: value.TotalTokens}
}

func (s *Service) renderPrompts(agent config.Agent, data map[string]any) (string, string, error) {
	system, err := s.renderPrompt(agent.PromptEngineering.SystemContent, agent.PromptEngineering.System, data)
	if err != nil {
		return "", "", err
	}
	system = strings.TrimSpace(system) + "\n\n" + envelopeInstructions(agent.Name)
	user, err := s.renderPrompt(agent.PromptEngineering.UserContent, agent.PromptEngineering.User, data)
	if err != nil {
		return "", "", err
	}
	audit := filepath.Join(s.taskDir(dataTask(data)), "prompts", agent.Name)
	if err = os.MkdirAll(audit, 0o700); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(audit, "system.md"), []byte(system), 0o600); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(audit, "user.md"), []byte(user), 0o600); err != nil {
		return "", "", err
	}
	return system, user, nil
}

func (s *Service) renderPrompt(content, path string, data map[string]any) (string, error) {
	if content != "" {
		parsed, err := template.New("frozen-prompt").Option("missingkey=zero").Parse(content)
		if err != nil {
			return "", err
		}
		var output strings.Builder
		if err = parsed.Execute(&output, data); err != nil {
			return "", err
		}
		return output.String(), nil
	}
	base := filepath.Dir(s.configPath)
	body, err := os.ReadFile(filepath.Join(base, path))
	if err != nil {
		return "", err
	}
	parsed, err := template.New(filepath.Base(path)).Option("missingkey=zero").Parse(string(body))
	if err != nil {
		return "", err
	}
	var output strings.Builder
	if err = parsed.Execute(&output, data); err != nil {
		return "", err
	}
	return output.String(), nil
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
	if err := s.executionGuard(ctx, taskID); err != nil {
		return store.Phase{}, err
	}
	phases, err := s.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, err
	}
	if err = s.ensureBranch(ctx, taskID, ""); err != nil {
		return store.Phase{}, err
	}
	task, err := s.db.Task(ctx, taskID)
	if err != nil {
		return store.Phase{}, err
	}
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
	inputs := make([]store.PhaseRepositoryInput, 0, len(task.Repositories))
	for _, repository := range task.Repositories {
		if repository.WorkingPath == "" {
			continue
		}
		head, headErr := factorygit.Head(ctx, s.git, repository.WorkingPath)
		if headErr != nil {
			return store.Phase{}, headErr
		}
		branch, branchErr := factorygit.Branch(ctx, s.git, repository.WorkingPath)
		if branchErr != nil {
			return store.Phase{}, branchErr
		}
		inputs = append(inputs, store.PhaseRepositoryInput{PhaseID: phase.ID, RepositoryID: repository.ID, ReviewBaseSHA: repositoryReviewBase(repository), HeadSHA: head, BranchName: branch})
	}
	entry := session.NewPhaseStart(session.PhasePayload{Phase: phase.ID, Name: name, Owner: owner, Kind: kind, InputSnapshot: inputSnapshot})
	event := store.Event{ID: randomID(), TaskID: taskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display, AvailableActions: AvailableActions(&phase, ""), StartedAt: time.Now().UTC()}
	if err = s.db.CommitPhaseStart(ctx, phase, inputs, event, s.taskDir(taskID)); err != nil {
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
	return s.endPhaseToState(ctx, phase, status, cause, "", "")
}

func (s *Service) endPhaseToState(ctx context.Context, phase store.Phase, status string, cause error, fromState, toState string, envelopes ...store.Envelope) error {
	var envelope *store.Envelope
	if len(envelopes) > 0 {
		envelope = &envelopes[0]
	}
	return s.endPhasePublication(ctx, phase, status, cause, fromState, toState, envelope, nil)
}

func (s *Service) endPhasePublication(ctx context.Context, phase store.Phase, status string, cause error, fromState, toState string, envelope *store.Envelope, changes []store.TestChange) error {
	if status == "success" {
		if err := s.executionGuard(ctx, phase.TaskID); err != nil {
			return err
		}
	}
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
	if outputSnapshot == "" {
		outputSnapshot = phase.InputSnapshot
	}
	phase.OutputSnapshot = outputSnapshot
	entry := session.NewPhaseEnd(session.PhasePayload{Phase: phase.ID, Name: phase.Name, Owner: phase.Owner, Kind: phase.Kind, Status: status, Error: message, InputSnapshot: phase.InputSnapshot, OutputSnapshot: phase.OutputSnapshot})
	event := store.Event{ID: randomID(), TaskID: phase.TaskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Kind: entry.Kind, Name: entry.Name, Payload: entry.Payload, Display: entry.Display, AvailableActions: AvailableActions(&phase, ""), StartedAt: time.Now().UTC()}
	var transition *store.TaskTransition
	if fromState != "" || toState != "" {
		transition = &store.TaskTransition{TaskID: phase.TaskID, FromState: fromState, ToState: toState}
	}
	if envelope != nil {
		return s.db.CommitAgentPhaseLifecycleWithEvidence(ctx, phase, status, message, outputSnapshot, transition, *envelope, changes, event, s.taskDir(phase.TaskID))
	}
	return s.db.CommitPhaseLifecycle(ctx, phase, status, message, outputSnapshot, transition, event, s.taskDir(phase.TaskID))
}

func (s *Service) failPhase(ctx context.Context, phase store.Phase, cause error) {
	_ = s.endPhase(context.Background(), phase, "failed", cause)
}

func (s *Service) executionGuard(ctx context.Context, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	executionID, ok := executionID(ctx)
	if !ok {
		return nil
	}
	if !s.executions.current(taskID, executionID) {
		return ErrStaleExecution
	}
	return nil
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
func (r *runtime) taskDir(id string) string { return filepath.Join(r.root, "tasks", id) }

func taskRepositories(taskID string, inputs []Repository, createdAt string) ([]store.TaskRepository, error) {
	values := make([]store.TaskRepository, 0, len(inputs))
	seen := make(map[string]bool, len(inputs))
	primaryCount := 0
	for index, input := range inputs {
		name, source, submitted, err := normalizeRepository(input)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("repository name %q is duplicated", name)
		}
		seen[name] = true
		primary := input.Primary
		if len(inputs) == 1 || index == 0 && !hasExplicitPrimary(inputs) {
			primary = true
		}
		if primary {
			primaryCount++
		}
		values = append(values, store.TaskRepository{ID: randomID(), TaskID: taskID, Name: name, SourceType: input.Type, SourceValue: source, SubmittedPath: submitted, Primary: primary, CreatedAt: createdAt})
	}
	if primaryCount != 1 {
		return nil, fmt.Errorf("repositories require exactly one primary")
	}
	return values, nil
}

func normalizeRepository(input Repository) (name, source, submitted string, err error) {
	switch input.Type {
	case "local":
		if !filepath.IsAbs(input.Path) {
			return "", "", "", fmt.Errorf("local repository path must be absolute")
		}
		source, submitted = input.Path, input.Path
	case "github":
		if parts := strings.Split(input.Repo, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", "", fmt.Errorf("github repository must be owner/repository")
		}
		source = input.Repo
	default:
		return "", "", "", fmt.Errorf("repository type must be local or github")
	}
	name = strings.TrimSpace(input.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(source), ".git")
	}
	if name == "." || name == ".." || name == "" || strings.ContainsAny(name, `/\\`) {
		return "", "", "", fmt.Errorf("repository name must be one path segment")
	}
	return name, source, submitted, nil
}

func hasExplicitPrimary(repositories []Repository) bool {
	for _, repository := range repositories {
		if repository.Primary {
			return true
		}
	}
	return false
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

func (s *Service) prepareRepositories(ctx context.Context, task store.Task, phase store.Phase) (string, error) {
	primaryPath := ""
	prepared, err := s.workspace.Prepare(ctx, task)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return "", err
	}
	for _, value := range prepared {
		repository := value.Repository
		profile := value.Profile
		if repository.Primary && len(profile.Checks) == 0 {
			err = fmt.Errorf("primary repository has no deterministic checks declared or detected")
			s.failPhase(ctx, phase, err)
			return "", err
		}
		if repository.Primary {
			primaryPath = repository.WorkingPath
		}
	}
	if primaryPath == "" {
		err := fmt.Errorf("primary repository is missing")
		s.failPhase(ctx, phase, err)
		return "", err
	}
	return primaryPath, nil
}

func primaryRepositoryName(task store.Task) string {
	for _, repository := range task.Repositories {
		if repository.Primary {
			return repository.Name
		}
	}
	return ""
}

func taskChangedFiles(ctx context.Context, runner factorygit.Runner, repositories []store.TaskRepository, reviewBase bool) ([]string, error) {
	var changed []string
	for _, repository := range repositories {
		if repository.WorkingPath == "" {
			continue
		}
		base := repository.BaseSHA
		if reviewBase && repository.ReviewBaseSHA != "" {
			base = repository.ReviewBaseSHA
		}
		files, err := factorygit.ChangedFiles(ctx, runner, repository.WorkingPath, base)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			changed = append(changed, repository.Name+"/"+file)
		}
	}
	return changed, nil
}

func repositoryReviewBase(repository store.TaskRepository) string {
	if repository.ReviewBaseSHA != "" {
		return repository.ReviewBaseSHA
	}
	return repository.BaseSHA
}

func repositoryFingerprints(ctx context.Context, runner factorygit.Runner, repositories []store.TaskRepository) (map[string]string, error) {
	values := make(map[string]string, len(repositories))
	for _, repository := range repositories {
		if repository.WorkingPath == "" {
			continue
		}
		fingerprint, err := factorygit.Fingerprint(ctx, runner, repository.WorkingPath)
		if err != nil {
			return nil, fmt.Errorf("fingerprint repository %s: %w", repository.Name, err)
		}
		values[repository.ID] = fingerprint
	}
	return values, nil
}

func sameFingerprints(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
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
