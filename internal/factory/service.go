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

	"github.com/jurabek/software-factory/internal/config"
	factorygit "github.com/jurabek/software-factory/internal/git"
	"github.com/jurabek/software-factory/internal/harness"
	"github.com/jurabek/software-factory/internal/store"
)

const maxCapturedOutput = 64 << 10

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
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
	Repo string `json:"repo,omitempty"`
}
type CreateRequest struct {
	Request    string     `json:"request"`
	Repository Repository `json:"repository"`
}
type Diff struct {
	Files []string `json:"files"`
	Patch string   `json:"patch"`
}

func NewService(root string, db *store.DB, cfg config.Config, configPath string, harnesses harness.Registry, gitRunner factorygit.Runner) *Service {
	return &Service{root: root, db: db, config: cfg, configPath: configPath, harnesses: harnesses, git: gitRunner, cancel: map[string]context.CancelFunc{}}
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (store.Campaign, error) {
	request.Request = strings.TrimSpace(request.Request)
	if request.Request == "" {
		return store.Campaign{}, fmt.Errorf("feature request is required")
	}
	var value, submitted string
	switch request.Repository.Type {
	case "local":
		if !filepath.IsAbs(request.Repository.Path) {
			return store.Campaign{}, fmt.Errorf("local repository path must be absolute")
		}
		value = request.Repository.Path
		submitted = value
	case "github":
		if !strings.Contains(request.Repository.Repo, "/") {
			return store.Campaign{}, fmt.Errorf("github repository must be owner/repository")
		}
		value = request.Repository.Repo
	default:
		return store.Campaign{}, fmt.Errorf("repository type must be local or github")
	}
	id, err := campaignID()
	if err != nil {
		return store.Campaign{}, err
	}
	campaign := store.Campaign{ID: id, Request: request.Request, RepositoryType: request.Repository.Type, RepositoryValue: value, SubmittedPath: submitted, State: string(Draft), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.db.CreateCampaign(ctx, campaign); err != nil {
		return store.Campaign{}, err
	}
	return campaign, nil
}

func (s *Service) Start(ctx context.Context, id string) error {
	if err := s.db.Claim(ctx, id, string(Draft), string(Preparing)); err != nil {
		return err
	}
	s.launch(id, s.prepareAndPlan)
	return nil
}

func (s *Service) Approve(ctx context.Context, id, actor string) error {
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	if campaign.State != string(AwaitingApproval) {
		return store.ErrConflict
	}
	payload, err := s.db.ValidEnvelope(ctx, id, "planner")
	if err != nil {
		return err
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

func (s *Service) Pause(ctx context.Context, id string) error {
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	state := State(campaign.State)
	if !CanTransition(state, Paused) {
		return store.ErrConflict
	}
	s.stop(id)
	return s.db.Transition(ctx, id, campaign.State, string(Paused), campaign.ActivePhase, "")
}

func (s *Service) Abort(ctx context.Context, id string) error {
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	if !CanTransition(State(campaign.State), Aborted) {
		return store.ErrConflict
	}
	s.stop(id)
	return s.db.Transition(ctx, id, campaign.State, string(Aborted), campaign.ActivePhase, "")
}

func (s *Service) Resume(ctx context.Context, id string) error {
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	if campaign.State != string(Paused) && campaign.State != string(Blocked) {
		return store.ErrConflict
	}
	target := State(campaign.PreviousState)
	if target == AwaitingApproval || target == Draft || target == "" {
		return store.ErrConflict
	}
	if !CanTransition(State(campaign.State), target) {
		return store.ErrConflict
	}
	if err := s.db.Transition(ctx, id, campaign.State, string(target), campaign.ActivePhase, ""); err != nil {
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
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	if isActive(State(campaign.State)) {
		return store.ErrConflict
	}
	if campaign.RepositoryType == "local" && campaign.WorkspacePath != "" {
		_, _ = s.git.Run(ctx, "git", "-C", campaign.CanonicalPath, "worktree", "remove", "--force", campaign.WorkspacePath)
	}
	if err := os.RemoveAll(s.campaignDir(id)); err != nil {
		return fmt.Errorf("remove campaign files: %w", err)
	}
	return s.db.DeleteCampaign(ctx, id)
}

func (s *Service) Diff(ctx context.Context, id string) (Diff, error) {
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return Diff{}, err
	}
	if campaign.WorkspacePath == "" {
		return Diff{}, nil
	}
	files, err := factorygit.ChangedFiles(ctx, s.git, campaign.WorkspacePath)
	if err != nil {
		return Diff{}, err
	}
	patch, err := factorygit.Diff(ctx, s.git, campaign.WorkspacePath)
	return Diff{Files: files, Patch: patch}, err
}

func (s *Service) launch(id string, run func(context.Context, string) error) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel[id] = cancel
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.cancel, id); s.mu.Unlock() }()
		if err := run(ctx, id); err != nil && !errors.Is(err, context.Canceled) {
			campaign, getErr := s.db.Campaign(context.Background(), id)
			if getErr == nil && campaign.State != string(Paused) && campaign.State != string(Aborted) && campaign.State != string(Blocked) {
				_ = s.db.Transition(context.Background(), id, campaign.State, string(Blocked), campaign.ActivePhase, err.Error())
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
		campaign, err := s.db.Campaign(ctx, id)
		if err == nil && isActive(State(campaign.State)) {
			_ = s.db.Transition(ctx, id, campaign.State, string(Blocked), campaign.ActivePhase, "server shutting down")
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
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	workspace := filepath.Join(s.campaignDir(id), "repository")
	if campaign.State == string(Planning) && campaign.WorkspacePath != "" {
		return s.plan(ctx, campaign)
	}
	phase, err := s.beginPhase(ctx, id, "prepare", "git", "factory", "Prepare repository")
	if err != nil {
		return err
	}
	var profile factorygit.Profile
	if campaign.RepositoryType == "local" {
		profile, err = factorygit.PrepareLocal(ctx, s.git, campaign.RepositoryValue, workspace)
	} else {
		profile, err = factorygit.PrepareGitHub(ctx, s.git, campaign.RepositoryValue, workspace)
	}
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	profile.Root = workspace
	encoded, _ := json.MarshalIndent(profile, "", "  ")
	if err = os.WriteFile(filepath.Join(s.campaignDir(id), "repository-profile.json"), encoded, 0o600); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	snapshot, err := os.ReadFile(s.configPath)
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	if err = os.WriteFile(filepath.Join(s.campaignDir(id), "config-snapshot.yaml"), snapshot, 0o600); err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	canonical := profile.Root
	if campaign.RepositoryType == "local" {
		canonical, _, _ = factorygit.ResolveRoot(ctx, s.git, campaign.RepositoryValue)
	}
	if err = s.db.SetPrepared(ctx, id, canonical, workspace, profile.BaseSHA, string(snapshot)); err != nil {
		return err
	}
	if err = s.endPhase(ctx, phase, "success", nil); err != nil {
		return err
	}
	if err = s.db.Transition(ctx, id, string(Preparing), string(Planning), "", ""); err != nil {
		return err
	}
	campaign, err = s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	return s.plan(ctx, campaign)
}

func (s *Service) plan(ctx context.Context, campaign store.Campaign) error {
	before, err := factorygit.ChangedFiles(ctx, s.git, campaign.WorkspacePath)
	if err != nil {
		return err
	}
	phase, err := s.beginPhase(ctx, campaign.ID, "planning", "agent", "planner", "Create implementation plan")
	if err != nil {
		return err
	}
	payload, err := s.runRole(ctx, campaign, phase, "planner", map[string]any{"CampaignID": campaign.ID, "Request": campaign.Request, "Repository": campaign.WorkspacePath}, func(text string) (any, error) { return ValidatePlan(text) })
	if err != nil {
		s.failPhase(ctx, phase, err)
		return err
	}
	after, err := factorygit.ChangedFiles(ctx, s.git, campaign.WorkspacePath)
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
	return s.db.Transition(ctx, campaign.ID, string(Planning), string(AwaitingApproval), "", "")
}

func (s *Service) buildCheckReview(ctx context.Context, id string) error {
	campaign, err := s.db.Campaign(ctx, id)
	if err != nil {
		return err
	}
	profile, err := readProfile(s.campaignDir(id))
	if err != nil {
		return err
	}
	plan, err := s.db.ValidEnvelope(ctx, id, "planner")
	if err != nil {
		return err
	}
	if campaign.State == string(Building) {
		phase, beginErr := s.beginPhase(ctx, id, "building", "agent", "builder", "Implement approved plan")
		if beginErr != nil {
			return beginErr
		}
		_, err = s.runRole(ctx, campaign, phase, "builder", map[string]any{"CampaignID": campaign.ID, "Request": campaign.Request, "Plan": plan}, func(text string) (any, error) { return ValidateBuild(text) })
		if err != nil {
			s.failPhase(ctx, phase, err)
			return err
		}
		files, diffErr := factorygit.ChangedFiles(ctx, s.git, campaign.WorkspacePath)
		if diffErr != nil {
			return diffErr
		}
		for _, file := range files {
			if factorygit.MatchesPath(file, profile.Protected) {
				err = fmt.Errorf("builder changed protected path %s", file)
				s.failPhase(ctx, phase, err)
				return err
			}
		}
		if err = s.endPhase(ctx, phase, "success", nil); err != nil {
			return err
		}
		if err = s.db.Transition(ctx, id, string(Building), string(Checking), "", ""); err != nil {
			return err
		}
	}
	campaign, _ = s.db.Campaign(ctx, id)
	if campaign.State == string(Checking) {
		phase, beginErr := s.beginPhase(ctx, id, "checks", "check", "factory", "Run deterministic checks")
		if beginErr != nil {
			return beginErr
		}
		if err = s.runChecks(ctx, campaign, phase, profile.Checks); err != nil {
			s.failPhase(ctx, phase, err)
			return err
		}
		if err = s.endPhase(ctx, phase, "success", nil); err != nil {
			return err
		}
		if err = s.db.Transition(ctx, id, string(Checking), string(Reviewing), "", ""); err != nil {
			return err
		}
	}
	campaign, _ = s.db.Campaign(ctx, id)
	before, err := factorygit.ChangedFiles(ctx, s.git, campaign.WorkspacePath)
	if err != nil {
		return err
	}
	phase, err := s.beginPhase(ctx, id, "reviewing", "agent", "reviewer", "Review implementation")
	if err != nil {
		return err
	}
	checks, _ := s.db.Checks(ctx, id)
	files, _ := factorygit.ChangedFiles(ctx, s.git, campaign.WorkspacePath)
	reviewPayload, err := s.runRole(ctx, campaign, phase, "reviewer", map[string]any{"CampaignID": campaign.ID, "Request": campaign.Request, "Plan": plan, "Checks": checks, "ChangedFiles": files}, func(text string) (any, error) { return ValidateReview(text) })
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
	after, _ := factorygit.ChangedFiles(ctx, s.git, campaign.WorkspacePath)
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

func (s *Service) runRole(ctx context.Context, campaign store.Campaign, phase store.Phase, role string, data map[string]any, validate validator) (string, error) {
	agent, ok := s.agent(role)
	if !ok {
		return "", fmt.Errorf("agent %s not configured", role)
	}
	adapter, ok := s.harnesses.Get(s.config.Defaults.CodingAgent)
	if !ok {
		return "", fmt.Errorf("harness %s unavailable", s.config.Defaults.CodingAgent)
	}
	systemPrompt, userPrompt, err := s.renderPrompts(agent, data)
	if err != nil {
		return "", err
	}
	sessionID := campaign.ID + "-" + role
	sessionDir := filepath.Join(s.campaignDir(campaign.ID), "sessions", role, "pi")
	rawPath := filepath.Join(s.campaignDir(campaign.ID), "sessions", role, "raw-output.jsonl")
	request := harness.Request{CWD: campaign.WorkspacePath, Prompt: userPrompt, SystemPrompt: systemPrompt, Model: agent.Model, Thinking: agent.Thinking, Tools: agent.Tools, SessionID: sessionID, SessionDirectory: sessionDir, RawOutputPath: rawPath, DeadlineMS: s.config.Runtime.AgentDeadlineMS}
	for attempt := 0; attempt <= s.config.Runtime.JSONFixAttempts; attempt++ {
		if attempt > 0 {
			request.Prompt = "Your previous final response was invalid. Return only the required " + role + " JSON object with every required field."
		}
		result, runErr := adapter.Run(ctx, request, s.eventSink(campaign.ID, phase.ID))
		if runErr != nil {
			return "", runErr
		}
		_ = s.db.SaveAgentSession(ctx, campaign.ID, role, s.config.Defaults.CodingAgent, result.Provider, result.Model, agent.Thinking, agent.Color, sessionID, sessionDir, result.ContextTokens, result.ContextWindow, result.Usage, result.Usage.Cost)
		_, validationErr := validate(result.Text)
		valid := validationErr == nil
		tail := result.Text
		if len(tail) > maxCapturedOutput {
			tail = tail[len(tail)-maxCapturedOutput:]
		}
		_ = s.db.SaveEnvelope(ctx, randomID(), campaign.ID, phase.ID, role, role, tail, valid, attempt+1)
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
	audit := filepath.Join(s.campaignDir(dataCampaign(data)), "prompts", agent.Name)
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
func dataCampaign(data map[string]any) string { return fmt.Sprint(data["CampaignID"]) }

func (s *Service) runChecks(ctx context.Context, campaign store.Campaign, phase store.Phase, checks []factorygit.Check) error {
	for _, spec := range checks {
		started := time.Now()
		artifact := filepath.Join(s.campaignDir(campaign.ID), "checks", spec.ID+".log")
		if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(artifact, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		tail := &tailCapture{limit: maxCapturedOutput}
		cmd := exec.Command("/bin/sh", "-c", spec.Command)
		cmd.Dir = campaign.WorkspacePath
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdout = io.MultiWriter(file, tail)
		cmd.Stderr = io.MultiWriter(file, tail)
		if err = cmd.Start(); err != nil {
			file.Close()
			return fmt.Errorf("start check %s: %w", spec.ID, err)
		}
		_, _ = s.db.StartProcess(ctx, campaign.ID, phase.ID, "check", spec.ID, cmd.Process.Pid, "/bin/sh -c "+spec.Command)
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
		_ = s.db.EndProcess(context.Background(), campaign.ID, cmd.Process.Pid, exit)
		ended := time.Now()
		_ = s.db.SaveCheck(context.Background(), store.Check{ID: spec.ID, CampaignID: campaign.ID, PhaseID: phase.ID, Name: spec.ID, Command: spec.Command, Attempt: 1, Status: status, ExitCode: exit, Output: tail.String(), ArtifactPath: artifact, DurationMS: int(ended.Sub(started).Milliseconds()), StartedAt: started.UTC().Format(time.RFC3339Nano), EndedAt: ended.UTC().Format(time.RFC3339Nano)})
		if err != nil {
			return fmt.Errorf("check %s failed: %w", spec.ID, err)
		}
	}
	return nil
}

func (s *Service) beginPhase(ctx context.Context, campaignID, name, kind, owner, description string) (store.Phase, error) {
	phases, err := s.db.Phases(ctx, campaignID)
	if err != nil {
		return store.Phase{}, err
	}
	phase := store.Phase{ID: randomID(), CampaignID: campaignID, Sequence: len(phases) + 1, Name: name, Kind: kind, Owner: owner, Description: description, Status: "running", Attempt: 1}
	if err = s.db.AddPhase(ctx, phase); err != nil {
		return store.Phase{}, err
	}
	campaign, _ := s.db.Campaign(ctx, campaignID)
	_ = s.db.Transition(ctx, campaignID, campaign.State, campaign.State, phase.ID, "")
	_ = s.trace(ctx, campaignID, phase.ID, "phase_start", name, map[string]any{"owner": owner, "kind": kind})
	return phase, nil
}

func (s *Service) endPhase(ctx context.Context, phase store.Phase, status string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	if err := s.db.EndPhase(ctx, phase.ID, status, message); err != nil {
		return err
	}
	return s.trace(ctx, phase.CampaignID, phase.ID, "phase_end", phase.Name, map[string]any{"status": status, "error": message})
}

func (s *Service) failPhase(ctx context.Context, phase store.Phase, cause error) {
	_ = s.endPhase(context.Background(), phase, "failed", cause)
}

func (s *Service) eventSink(campaignID, phaseID string) harness.EventSink {
	return func(ctx context.Context, event harness.Event) error {
		pid := payloadInt(event.Payload, "pid")
		if event.Type == "process_start" {
			_, _ = s.db.StartProcess(ctx, campaignID, phaseID, "pi", event.Name, pid, fmt.Sprint(event.Payload["command"]))
		}
		if event.Type == "process_end" {
			_ = s.db.EndProcess(ctx, campaignID, pid, payloadInt(event.Payload, "exit_code"))
		}
		return s.trace(ctx, campaignID, phaseID, event.Type, event.Name, event.Payload)
	}
}

func (s *Service) trace(ctx context.Context, campaignID, phaseID, eventType, name string, payload any) error {
	_, err := s.db.AppendEvent(ctx, s.campaignDir(campaignID), store.Event{ID: randomID(), CampaignID: campaignID, PhaseID: phaseID, Type: eventType, Name: name, Payload: payload, StartedAt: time.Now().UTC()})
	return err
}

func (s *Service) agent(role string) (config.Agent, bool) {
	for _, agent := range s.config.Agents {
		if agent.Name == role {
			return agent, true
		}
	}
	return config.Agent{}, false
}
func (s *Service) campaignDir(id string) string { return filepath.Join(s.root, "campaigns", id) }

func campaignID() (string, error) {
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

func readProfile(dir string) (factorygit.Profile, error) {
	body, err := os.ReadFile(filepath.Join(dir, "repository-profile.json"))
	if err != nil {
		return factorygit.Profile{}, err
	}
	var profile factorygit.Profile
	err = json.Unmarshal(body, &profile)
	return profile, err
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
