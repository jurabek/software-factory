package factory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"gopkg.in/yaml.v3"
)

const maxCapturedOutput = 64 << 10

var (
	ErrStalePlan       = errors.New("plan digest is stale")
	ErrInvalidFeedback = errors.New("feedback is required")
)

type Service struct {
	root       string
	db         *store.DB
	config     config.Config
	configPath string
	harnesses  harness.Registry
	git        factorygit.Runner
	mu         sync.Mutex
	cancel     map[string]context.CancelFunc
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
	CodingAgent  string       `json:"coding_agent,omitempty"`
	Model        string       `json:"model,omitempty"`
	Thinking     string       `json:"thinking,omitempty"`
}
type CreateSessionRequest struct {
	Request string `json:"request"`
}
type InterventionRequest struct {
	TargetType     string `json:"target_type"`
	TargetID       string `json:"target_id"`
	Message        string `json:"message"`
	IdempotencyKey string `json:"idempotency_key"`
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

func NewService(root string, db *store.DB, cfg config.Config, configPath string, harnesses harness.Registry, gitRunner factorygit.Runner) *Service {
	return &Service{root: root, db: db, config: cfg, configPath: configPath, harnesses: harnesses, git: gitRunner, cancel: map[string]context.CancelFunc{}}
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (store.Task, error) {
	return s.create(ctx, request, "")
}

func (s *Service) CreateSession(ctx context.Context, taskID string, request CreateSessionRequest) (store.Task, error) {
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
	}, task.ID)
}

func (s *Service) create(ctx context.Context, request CreateRequest, parentTaskID string) (store.Task, error) {
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
	if request.CodingAgent != "" && !config.IsValidHarness(request.CodingAgent) {
		return store.Task{}, fmt.Errorf("coding_agent must be pi or codex")
	}
	if request.Thinking != "" && !config.IsValidThinking(request.Thinking) {
		return store.Task{}, fmt.Errorf("thinking is invalid")
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
	for _, directory := range []string{workspace, filepath.Join(workspace, "workspace", "repositories"), filepath.Join(workspace, "attempts"), filepath.Join(workspace, "snapshots"), filepath.Join(workspace, "artifacts"), filepath.Join(workspace, "sessions"), filepath.Join(workspace, "workspace", "snapshots"), filepath.Join(workspace, "workspace", "branches"), filepath.Join(workspace, "workspace", "attempts")} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			_ = os.RemoveAll(workspace)
			return store.Task{}, fmt.Errorf("create task workspace: %w", err)
		}
	}
	task := store.Task{ID: id, ParentTaskID: parentTaskID, Request: request.Request, WorkspacePath: workspace, Repositories: repositories, State: string(Draft), CreatedAt: createdAt, CodingAgent: request.CodingAgent, Model: request.Model, Thinking: request.Thinking}
	metadata, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("encode task metadata: %w", err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "task.json"), metadata, 0o600); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, fmt.Errorf("write task metadata: %w", err)
	}
	if err := s.db.CreateTask(ctx, task); err != nil {
		_ = os.RemoveAll(workspace)
		return store.Task{}, err
	}
	return task, nil
}

func (s *Service) Start(ctx context.Context, id string) error {
	if err := s.db.Claim(ctx, id, string(Draft), string(Preparing)); err != nil {
		return err
	}
	if err := s.ensureBranch(ctx, id, ""); err != nil {
		return err
	}
	s.launch(id, s.prepareAndPlan)
	return nil
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

func (s *Service) Comment(ctx context.Context, taskID, actor string, request InterventionRequest) (store.Intervention, error) {
	request.Message = strings.TrimSpace(request.Message)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.Message == "" {
		return store.Intervention{}, fmt.Errorf("message is required")
	}
	if request.IdempotencyKey == "" {
		return store.Intervention{}, fmt.Errorf("idempotency_key is required")
	}
	switch request.TargetType {
	case "task", "attempt", "event", "artifact":
	default:
		return store.Intervention{}, fmt.Errorf("target_type must be task, attempt, event, or artifact")
	}
	if strings.TrimSpace(request.TargetID) == "" {
		return store.Intervention{}, fmt.Errorf("target_id is required")
	}
	if _, err := s.db.Task(ctx, taskID); err != nil {
		return store.Intervention{}, err
	}
	value := store.Intervention{ID: randomID(), TaskID: taskID, TargetType: request.TargetType, TargetID: request.TargetID, Actor: actor, Intent: "comment", Text: request.Message, Delivery: "applied", IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	stored, created, err := s.db.SaveIntervention(ctx, value)
	if err != nil {
		return store.Intervention{}, err
	}
	if created {
		_ = s.trace(ctx, taskID, "", "intervention", "Comment", map[string]any{"intervention_id": stored.ID, "target_type": stored.TargetType, "target_id": stored.TargetID, "message": stored.Text})
	}
	return stored, nil
}

func (s *Service) Approve(ctx context.Context, id, actor string) error {
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
	if err := s.db.SetApproval(ctx, id, hex.EncodeToString(digest[:]), actor); err != nil {
		return err
	}
	if err := s.db.Transition(ctx, id, string(AwaitingApproval), string(Building), "", ""); err != nil {
		return err
	}
	s.launch(id, s.buildCheckReview)
	return nil
}

func (s *Service) Feedback(ctx context.Context, id, actor, text, digest string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return ErrInvalidFeedback
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
	current := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	if digest != "" && digest != current {
		return ErrStalePlan
	}
	if err = s.db.SaveFeedback(ctx, store.Feedback{ID: randomID(), TaskID: id, Actor: actor, PlanDigest: current, Text: text, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	if err = s.trace(ctx, id, "", "plan_feedback", "Planner feedback", map[string]any{"actor": actor, "plan_digest": current, "feedback": text}); err != nil {
		return err
	}
	if err = s.db.Transition(ctx, id, string(AwaitingApproval), string(Planning), "", ""); err != nil {
		return err
	}
	s.launch(id, s.revisePlan)
	return nil
}

func (s *Service) revisePlan(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	payload, err := s.db.ValidEnvelope(ctx, id, "planner")
	if err != nil {
		return err
	}
	feedback, err := s.db.Feedback(ctx, id)
	if err != nil || len(feedback) == 0 {
		return fmt.Errorf("feedback not found")
	}
	return s.plan(ctx, task, map[string]any{"CurrentPlan": payload, "Questions": mustPlanQuestions(payload), "Feedback": feedback[len(feedback)-1].Text})
}

func mustPlanQuestions(payload string) []string {
	plan, _ := ValidatePlan(payload)
	return plan.Questions
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
	s.stop(id)
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
	s.stop(id)
	return s.db.Transition(ctx, id, task.State, string(Aborted), task.ActivePhase, "")
}

func (s *Service) Resume(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	if task.State != string(Paused) && task.State != string(Blocked) {
		return store.ErrConflict
	}
	target := State(task.PreviousState)
	if target == AwaitingApproval || target == Draft || target == "" {
		return store.ErrConflict
	}
	if !CanTransition(State(task.State), target) {
		return store.ErrConflict
	}
	if err := s.db.Transition(ctx, id, task.State, string(target), task.ActivePhase, ""); err != nil {
		return err
	}
	if target == Preparing || target == Planning {
		s.launch(id, s.prepareAndPlan)
	} else {
		s.launch(id, s.buildCheckReview)
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
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
		for _, repository := range session.Repositories {
			if repository.SourceType == "local" && repository.WorkingPath != "" {
				_, _ = s.git.Run(ctx, "git", "-C", repository.CanonicalPath, "worktree", "remove", "--force", repository.WorkingPath)
			}
		}
		if err := os.RemoveAll(s.taskDir(session.ID)); err != nil {
			return fmt.Errorf("remove task files: %w", err)
		}
	}
	return s.db.DeleteTask(ctx, id)
}

func (s *Service) Diff(ctx context.Context, id string) (Diff, error) {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return Diff{}, err
	}
	result := Diff{Repositories: make([]RepositoryDiff, 0, len(task.Repositories))}
	for _, repository := range task.Repositories {
		if repository.WorkingPath == "" {
			continue
		}
		files, diffErr := factorygit.ChangedFiles(ctx, s.git, repository.WorkingPath)
		if diffErr != nil {
			return Diff{}, diffErr
		}
		patch, diffErr := factorygit.Diff(ctx, s.git, repository.WorkingPath)
		if diffErr != nil {
			return Diff{}, diffErr
		}
		result.Repositories = append(result.Repositories, RepositoryDiff{RepositoryID: repository.ID, Name: repository.Name, Files: files, Patch: patch})
	}
	return result, nil
}

func (s *Service) launch(id string, run func(context.Context, string) error) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel[id] = cancel
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.cancel, id); s.mu.Unlock() }()
		if err := run(ctx, id); err != nil && !errors.Is(err, context.Canceled) {
			task, getErr := s.db.Task(context.Background(), id)
			if getErr == nil && task.State != string(Paused) && task.State != string(Aborted) && task.State != string(Blocked) {
				_ = s.db.Transition(context.Background(), id, task.State, string(Blocked), task.ActivePhase, err.Error())
			}
		}
	}()
}

func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.cancel))
	for id, cancel := range s.cancel {
		cancel()
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		task, err := s.db.Task(ctx, id)
		if err == nil && isActive(State(task.State)) {
			_ = s.db.Transition(ctx, id, task.State, string(Blocked), task.ActivePhase, "server shutting down")
		}
	}
}

func (s *Service) stop(id string) {
	s.mu.Lock()
	cancel := s.cancel[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
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
	primaryPath := ""
	for _, repository := range task.Repositories {
		workingPath := filepath.Join(task.WorkspacePath, "workspace", "repositories", repository.Name)
		profile, prepareErr := s.prepareRepository(ctx, repository, workingPath)
		if prepareErr != nil {
			s.failPhase(ctx, phase, prepareErr)
			return prepareErr
		}
		if repository.Primary && len(profile.Checks) == 0 {
			prepareErr = fmt.Errorf("primary repository has no deterministic checks declared or detected")
			s.failPhase(ctx, phase, prepareErr)
			return prepareErr
		}
		canonical := profile.Root
		if repository.SourceType == "local" {
			canonical, _, prepareErr = factorygit.ResolveRoot(ctx, s.git, repository.SourceValue)
		}
		if prepareErr != nil {
			s.failPhase(ctx, phase, prepareErr)
			return prepareErr
		}
		repository.CanonicalPath, repository.WorkingPath, repository.BaseSHA = canonical, workingPath, profile.BaseSHA
		if err = s.db.SetRepositoryPrepared(ctx, repository); err != nil {
			s.failPhase(ctx, phase, err)
			return err
		}
		if err = writeRepositoryProfile(task.WorkspacePath, repository.Name, profile); err != nil {
			s.failPhase(ctx, phase, err)
			return err
		}
		if repository.Primary {
			primaryPath = workingPath
			encoded, encodeErr := json.MarshalIndent(profile, "", "  ")
			if encodeErr != nil {
				s.failPhase(ctx, phase, encodeErr)
				return encodeErr
			}
			if err = os.WriteFile(filepath.Join(task.WorkspacePath, "repository-profile.json"), encoded, 0o600); err != nil {
				s.failPhase(ctx, phase, err)
				return err
			}
		}
	}
	if primaryPath == "" {
		err = fmt.Errorf("primary repository is missing")
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = os.WriteFile(filepath.Join(s.taskDir(id), "config-snapshot.yaml"), snapshot, 0o600); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.db.SetPrepared(ctx, id, primaryPath, string(snapshot)); err != nil {
		return err
	}
	if err = s.endPhase(ctx, phase, "success", nil); err != nil {
		return err
	}
	if err = s.db.Transition(ctx, id, string(Preparing), string(Planning), "", ""); err != nil {
		return err
	}
	task, err = s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	return s.plan(ctx, task, nil)
}

func (s *Service) plan(ctx context.Context, task store.Task, revision map[string]any) error {
	before, err := taskChangedFiles(ctx, s.git, task.Repositories)
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
	payload, err := s.runRole(ctx, task, phase, "planner", data, func(text string) (any, error) { return ValidatePlan(text) })
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	after, err := taskChangedFiles(ctx, s.git, task.Repositories)
	if err != nil {
		return err
	}
	if !sameStrings(before, after) {
		err = fmt.Errorf("planner modified repository")
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.endPhase(ctx, phase, "success", nil); err != nil {
		return err
	}
	_ = payload
	return s.db.Transition(ctx, task.ID, string(Planning), string(AwaitingApproval), "", "")
}

func (s *Service) buildCheckReview(ctx context.Context, id string) error {
	task, err := s.db.Task(ctx, id)
	if err != nil {
		return err
	}
	profiles, err := readTaskProfiles(task)
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
		_, err = s.runRole(ctx, task, phase, "builder", map[string]any{"TaskID": task.ID, "Request": task.Request, "Plan": plan}, func(text string) (any, error) { return ValidateBuild(text) })
		if err != nil {
			s.failPhase(ctx, phase, err)
			return err
		}
		for _, repository := range task.Repositories {
			files, diffErr := factorygit.ChangedFiles(ctx, s.git, repository.WorkingPath)
			if diffErr != nil {
				return diffErr
			}
			for _, file := range files {
				if factorygit.MatchesPath(file, profiles[repository.Name].Protected) {
					err = fmt.Errorf("builder changed protected path %s/%s", repository.Name, file)
					s.failPhase(ctx, phase, err)
					return err
				}
			}
		}
		if err = s.endPhase(ctx, phase, "success", nil); err != nil {
			return err
		}
		if err = s.db.Transition(ctx, id, string(Building), string(Checking), "", ""); err != nil {
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
			if err = s.runChecks(ctx, task, phase, repository, profiles[repository.Name].Checks); err != nil {
				s.failPhase(ctx, phase, err)
				return err
			}
		}
		if err = s.endPhase(ctx, phase, "success", nil); err != nil {
			return err
		}
		if err = s.db.Transition(ctx, id, string(Checking), string(Reviewing), "", ""); err != nil {
			return err
		}
	}
	task, _ = s.db.Task(ctx, id)
	before, err := taskChangedFiles(ctx, s.git, task.Repositories)
	if err != nil {
		return err
	}
	phase, err := s.beginPhase(ctx, id, "reviewing", "agent", "reviewer", "Review implementation")
	if err != nil {
		return err
	}
	checks, _ := s.db.Checks(ctx, id)
	files, _ := taskChangedFiles(ctx, s.git, task.Repositories)
	reviewPayload, err := s.runRole(ctx, task, phase, "reviewer", map[string]any{"TaskID": task.ID, "Request": task.Request, "Plan": plan, "Checks": checks, "ChangedFiles": files}, func(text string) (any, error) { return ValidateReview(text) })
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	review, _ := ValidateReview(reviewPayload)
	if !review.Approved {
		err = fmt.Errorf("reviewer rejected implementation")
		s.failPhase(ctx, phase, err)
		return err
	}
	after, _ := taskChangedFiles(ctx, s.git, task.Repositories)
	if !sameStrings(before, after) {
		err = fmt.Errorf("reviewer modified repository")
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = s.endPhase(ctx, phase, "success", nil); err != nil {
		return err
	}
	return s.db.Transition(ctx, id, string(Reviewing), string(Completed), "", "")
}

type validator func(string) (any, error)

func (s *Service) runRole(ctx context.Context, task store.Task, phase store.Phase, role string, data map[string]any, validate validator) (string, error) {
	taskConfig, err := s.taskConfig(task)
	if err != nil {
		return "", err
	}
	agent, ok := agentForRole(taskConfig, role)
	if !ok {
		return "", fmt.Errorf("agent %s not configured", role)
	}
	adapter, ok := s.harnesses.Get(taskConfig.Defaults.CodingAgent)
	if !ok {
		return "", fmt.Errorf("harness %s unavailable", taskConfig.Defaults.CodingAgent)
	}
	systemPrompt, userPrompt, err := s.renderPrompts(agent, data)
	if err != nil {
		return "", err
	}
	sessionID := task.ID + "-" + role
	sessionDir := filepath.Join(s.taskDir(task.ID), "sessions", role, "pi")
	rawPath := filepath.Join(s.taskDir(task.ID), "sessions", role, "raw-output.jsonl")
	request := harness.Request{CWD: task.PrimaryRepositoryPath, Prompt: userPrompt, SystemPrompt: systemPrompt, Model: agent.Model, Thinking: agent.Thinking, SessionID: sessionID, SessionDirectory: sessionDir, RawOutputPath: rawPath, DeadlineMS: taskConfig.Runtime.AgentDeadlineMS}
	for attempt := 0; attempt <= taskConfig.Runtime.JSONFixAttempts; attempt++ {
		if attempt > 0 {
			request.Prompt = "Your previous final response was invalid. Return only the required " + role + " JSON object with every required field."
		}
		result, runErr := adapter.Run(ctx, request, s.eventSink(task.ID, phase.ID))
		if runErr != nil {
			return "", runErr
		}
		_ = s.db.SaveAgentSession(ctx, task.ID, role, taskConfig.Defaults.CodingAgent, result.Provider, result.Model, agent.Thinking, agent.Color, sessionID, sessionDir, result.ContextTokens, result.ContextWindow, result.Usage, result.Usage.Cost)
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
		_ = s.db.SaveEnvelope(ctx, randomID(), task.ID, phase.ID, role, role, stored, valid, attempt+1)
		if valid {
			return result.Text, nil
		}
		err = validationErr
	}
	return "", fmt.Errorf("%s envelope invalid after corrections: %w", role, err)
}

func (s *Service) renderPrompts(agent config.Agent, data map[string]any) (string, string, error) {
	base := filepath.Dir(s.configPath)
	render := func(path string) (string, error) {
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
	system, err := render(agent.PromptEngineering.System)
	if err != nil {
		return "", "", err
	}
	user, err := render(agent.PromptEngineering.User)
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
func dataTask(data map[string]any) string { return fmt.Sprint(data["TaskID"]) }

func (s *Service) runChecks(ctx context.Context, task store.Task, phase store.Phase, repository store.TaskRepository, checks []factorygit.Check) error {
	for _, spec := range checks {
		started := time.Now()
		checkID := repository.Name + ":" + spec.ID
		artifact := filepath.Join(s.taskDir(task.ID), "checks", repository.Name, spec.ID+".log")
		if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(artifact, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		tail := &tailCapture{limit: maxCapturedOutput}
		cmd := exec.Command("/bin/sh", "-c", spec.Command)
		cmd.Dir = repository.WorkingPath
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdout = io.MultiWriter(file, tail)
		cmd.Stderr = io.MultiWriter(file, tail)
		if err = cmd.Start(); err != nil {
			file.Close()
			return fmt.Errorf("start check %s: %w", spec.ID, err)
		}
		_, _ = s.db.StartProcess(ctx, task.ID, phase.ID, "check", checkID, cmd.Process.Pid, "/bin/sh -c "+spec.Command)
		done := make(chan struct{})
		go func(pid int) {
			select {
			case <-ctx.Done():
				_ = syscall.Kill(-pid, syscall.SIGTERM)
				time.Sleep(500 * time.Millisecond)
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			case <-done:
			}
		}(cmd.Process.Pid)
		err = cmd.Wait()
		close(done)
		_ = file.Sync()
		_ = file.Close()
		exit := 0
		status := "passed"
		if err != nil {
			exit = -1
			status = "failed"
			if value, ok := err.(*exec.ExitError); ok {
				exit = value.ExitCode()
			}
		}
		_ = s.db.EndProcess(context.Background(), task.ID, cmd.Process.Pid, exit)
		ended := time.Now()
		_ = s.db.SaveCheck(context.Background(), store.Check{ID: checkID, TaskID: task.ID, PhaseID: phase.ID, Name: checkID, Command: spec.Command, Attempt: 1, Status: status, ExitCode: exit, Output: tail.String(), ArtifactPath: artifact, DurationMS: int(ended.Sub(started).Milliseconds()), StartedAt: started.UTC().Format(time.RFC3339Nano), EndedAt: ended.UTC().Format(time.RFC3339Nano)})
		if err != nil {
			return fmt.Errorf("check %s failed: %w", spec.ID, err)
		}
	}
	return nil
}

func (s *Service) beginPhase(ctx context.Context, taskID, name, kind, owner, description string) (store.Phase, error) {
	phases, err := s.db.Phases(ctx, taskID)
	if err != nil {
		return store.Phase{}, err
	}
	_ = s.ensureBranch(ctx, taskID, "")
	task, _ := s.db.Task(ctx, taskID)
	definitionID := s.ensureDefinition(ctx, taskID, name, kind, owner)
	inputSnapshot := ""
	if snapshot, captureErr := s.CaptureSnapshot(ctx, store.Task{ID: taskID, WorkspacePath: s.taskDir(taskID)}); captureErr == nil {
		inputSnapshot = snapshot.Digest
	}
	phase := store.Phase{ID: randomID(), TaskID: taskID, Sequence: len(phases) + 1, Name: name, Kind: kind, Owner: owner, Description: description, Status: "running", Attempt: 1, BranchID: task.SelectedBranchID, DefinitionID: definitionID, InputSnapshot: inputSnapshot}
	if err = s.db.AddPhase(ctx, phase); err != nil {
		return store.Phase{}, err
	}
	if phase.BranchID != "" {
		_ = s.db.SetBranchHead(ctx, taskID, phase.BranchID, phase.ID)
	}
	_ = s.db.Transition(ctx, taskID, task.State, task.State, phase.ID, "")
	_ = s.traceBranch(ctx, taskID, phase, "phase_start", name, map[string]any{"owner": owner, "kind": kind, "input_snapshot": inputSnapshot})
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
	if task, taskErr := s.db.Task(ctx, phase.TaskID); taskErr == nil {
		if snapshot, captureErr := s.CaptureSnapshot(ctx, task); captureErr == nil {
			if status == "success" && (phase.Kind == "agent" || phase.Kind == "check" || phase.Kind == "git") {
				outputSnapshot = snapshot.Digest
			} else if status != "success" {
				outputSnapshot = snapshot.Digest
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
	if err := s.db.EndPhase(ctx, phase.ID, status, message); err != nil {
		return err
	}
	return s.traceBranch(ctx, phase.TaskID, phase, "phase_end", phase.Name, map[string]any{"status": status, "error": message, "output_snapshot": phase.OutputSnapshot})
}

func (s *Service) traceBranch(ctx context.Context, taskID string, phase store.Phase, eventType, name string, payload map[string]any) error {
	actions := AvailableActions(&phase, "")
	_, err := s.db.AppendEvent(ctx, s.taskDir(taskID), store.Event{ID: randomID(), TaskID: taskID, PhaseID: phase.ID, AttemptID: phase.ID, BranchID: phase.BranchID, Type: eventType, Name: name, Payload: payload, AvailableActions: actions, StartedAt: time.Now().UTC()})
	return err
}

func (s *Service) failPhase(ctx context.Context, phase store.Phase, cause error) {
	_ = s.endPhase(context.Background(), phase, "failed", cause)
}

func (s *Service) eventSink(taskID, phaseID string) harness.EventSink {
	return func(ctx context.Context, event harness.Event) error {
		pid := payloadInt(event.Payload, "pid")
		if event.Type == "process_start" {
			_, _ = s.db.StartProcess(ctx, taskID, phaseID, "pi", event.Name, pid, fmt.Sprint(event.Payload["command"]))
		}
		if event.Type == "process_end" {
			_ = s.db.EndProcess(ctx, taskID, pid, payloadInt(event.Payload, "exit_code"))
		}
		return s.trace(ctx, taskID, phaseID, event.Type, event.Name, event.Payload)
	}
}

func (s *Service) trace(ctx context.Context, taskID, phaseID, eventType, name string, payload any) error {
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
	_, err := s.db.AppendEvent(ctx, s.taskDir(taskID), store.Event{ID: randomID(), TaskID: taskID, PhaseID: phaseID, AttemptID: attemptID, BranchID: branchID, Type: eventType, Name: name, Payload: payload, AvailableActions: actions, StartedAt: time.Now().UTC()})
	return err
}

func (s *Service) taskConfig(task store.Task) (config.Config, error) {
	if task.ConfigSnapshot == "" {
		return s.config, nil
	}
	configured, problems, err := config.Parse([]byte(task.ConfigSnapshot), filepath.Dir(s.configPath))
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
	if !config.IsValidThinking(configured.Defaults.Thinking) {
		problems = append(problems, "defaults.thinking is invalid")
	}
	for _, role := range []string{"planner", "builder", "reviewer"} {
		found := false
		for _, a := range configured.Agents {
			if a.Name == role {
				found = true
				if !config.IsValidThinking(a.Thinking) {
					problems = append(problems, role+" thinking is invalid")
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

func readTaskProfiles(task store.Task) (map[string]factorygit.Profile, error) {
	profiles := make(map[string]factorygit.Profile, len(task.Repositories))
	for _, repository := range task.Repositories {
		body, err := os.ReadFile(filepath.Join(task.WorkspacePath, "repository-profiles", repository.Name+".json"))
		if err != nil {
			return nil, fmt.Errorf("read repository profile %s: %w", repository.Name, err)
		}
		var profile factorygit.Profile
		if err = json.Unmarshal(body, &profile); err != nil {
			return nil, fmt.Errorf("decode repository profile %s: %w", repository.Name, err)
		}
		profiles[repository.Name] = profile
	}
	return profiles, nil
}

func (s *Service) prepareRepository(ctx context.Context, repository store.TaskRepository, destination string) (factorygit.Profile, error) {
	if repository.SourceType == "local" {
		return factorygit.PrepareLocal(ctx, s.git, repository.SourceValue, destination)
	}
	return factorygit.PrepareGitHub(ctx, s.git, repository.SourceValue, destination)
}

func writeRepositoryProfile(taskWorkspace, name string, profile factorygit.Profile) error {
	encoded, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode repository profile: %w", err)
	}
	directory := filepath.Join(taskWorkspace, "repository-profiles")
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create repository profile directory: %w", err)
	}
	if err = os.WriteFile(filepath.Join(directory, name+".json"), encoded, 0o600); err != nil {
		return fmt.Errorf("write repository profile: %w", err)
	}
	return nil
}

func taskChangedFiles(ctx context.Context, runner factorygit.Runner, repositories []store.TaskRepository) ([]string, error) {
	var changed []string
	for _, repository := range repositories {
		if repository.WorkingPath == "" {
			continue
		}
		files, err := factorygit.ChangedFiles(ctx, runner, repository.WorkingPath)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			changed = append(changed, repository.Name+"/"+file)
		}
	}
	return changed, nil
}

func isActive(state State) bool {
	return state == Preparing || state == Planning || state == AwaitingApproval || state == Building || state == Checking || state == Reviewing
}

type tailCapture struct {
	data  []byte
	limit int
}

func (capture *tailCapture) Write(data []byte) (int, error) {
	length := len(data)
	capture.data = append(capture.data, data...)
	if len(capture.data) > capture.limit {
		capture.data = capture.data[len(capture.data)-capture.limit:]
	}
	return length, nil
}
func (capture *tailCapture) String() string { return string(capture.data) }
func payloadInt(payload map[string]any, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func sameStrings(left, right []string) bool {
	sort.Strings(left)
	sort.Strings(right)
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}
