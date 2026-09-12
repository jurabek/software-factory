package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/factory"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/sandbox"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/stretchr/testify/suite"
)

const (
	flowTaskRequest = "Create the build artifact"
	flowPlan        = `{"status":"success","summary":"Create the requested artifact","artifacts":[],"notes_for_next_agent":"","steps":[{"id":"build-artifact","description":"Create built.txt","expected_files":["built.txt"],"acceptance_criteria":["deterministic check passes"]}],"questions":[]}`
	flowBuild       = `{"status":"success","summary":"Created the build artifact","artifacts":[],"notes_for_next_agent":"","changed_files":["built.txt"],"commit_message":"Create build artifact","test_changes":[]}`
)

type taskFlowHarness struct {
	mu       sync.Mutex
	requests []harness.Request
}

func (h *taskFlowHarness) Models(context.Context) ([]harness.Model, error) {
	return []harness.Model{{Provider: "test", ID: "test/model"}}, nil
}

func (h *taskFlowHarness) Run(_ context.Context, request harness.Request, _ harness.EventSink) (harness.Result, error) {
	h.mu.Lock()
	h.requests = append(h.requests, request)
	invocation := len(h.requests)
	h.mu.Unlock()

	result := harness.Result{SessionID: request.SessionID, Provider: "test", Model: request.Model, SessionReady: true, AccountingComplete: true}
	switch invocation {
	case 1:
		if !strings.Contains(request.SystemPrompt, "Plan the task") || !strings.Contains(request.Prompt, flowTaskRequest) {
			return result, fmt.Errorf("planner prompt missing task handoff: system=%q user=%q", request.SystemPrompt, request.Prompt)
		}
		result.Text = flowPlan
	case 2:
		if !strings.Contains(request.SystemPrompt, "Build the approved plan") || !strings.Contains(request.Prompt, flowTaskRequest) || !strings.Contains(request.Prompt, "Create the requested artifact") {
			return result, fmt.Errorf("builder prompt missing approved plan handoff: system=%q user=%q", request.SystemPrompt, request.Prompt)
		}
		if err := os.WriteFile(filepath.Join(request.CWD, "built.txt"), []byte("built\n"), 0o600); err != nil {
			return result, err
		}
		result.Text = flowBuild
	default:
		return result, fmt.Errorf("unexpected harness invocation %d", invocation)
	}
	return result, nil
}

func (h *taskFlowHarness) Requests() []harness.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]harness.Request(nil), h.requests...)
}

type taskFlowSuite struct {
	suite.Suite
	client  *http.Client
	db      *store.DB
	harness *taskFlowHarness
	repo    string
	server  *httptest.Server
	service *factory.Service
}

func TestTaskFlowSuite(t *testing.T) {
	suite.Run(t, new(taskFlowSuite))
}

func (s *taskFlowSuite) SetupSuite() {
	root := s.T().TempDir()
	s.repo = filepath.Join(root, "repository")
	s.Require().NoError(os.MkdirAll(s.repo, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(s.repo, "AGENTS.md"), []byte("<!-- software-factory:start -->\n```yaml\nchecks:\n  - id: build-output\n    command: test \"$(cat built.txt)\" = \"built\"\n```\n<!-- software-factory:end -->\n"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(s.repo, "README"), []byte("fixture\n"), 0o600))
	s.git("init")
	s.git("add", ".")
	s.git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")

	configRoot := filepath.Join(root, "config")
	s.writePrompt(configRoot, "planner", "system.md", "Plan the task without editing files.")
	s.writePrompt(configRoot, "planner", "user.md", "Task {{.TaskID}}: {{.Request}} in {{.Repository}}")
	s.writePrompt(configRoot, "builder", "system.md", "Build the approved plan.")
	s.writePrompt(configRoot, "builder", "user.md", "Task {{.TaskID}}: {{.Request}}\nPlan: {{.Plan}}")

	cfg := config.Config{
		Defaults: config.Defaults{CodingAgent: "pi", Model: "test/model", Thinking: "low"},
		Runtime:  config.Runtime{AgentDeadlineMS: 5_000},
		Agents: []config.Agent{
			{Name: "planner", Model: "test/model", Thinking: "low", PromptEngineering: config.PromptEngineering{System: "prompts/planner/system.md", User: "prompts/planner/user.md"}},
			{Name: "builder", Model: "test/model", Thinking: "low", PromptEngineering: config.PromptEngineering{System: "prompts/builder/system.md", User: "prompts/builder/user.md"}},
		},
		Pipelines: []config.Pipeline{{Name: "standard", Default: true, Stages: []config.Stage{{ID: "build", Kind: "build", Agent: "builder"}, {ID: "checks", Kind: "verify"}}}},
	}
	var err error
	s.db, err = store.Open(filepath.Join(root, "factory.db"))
	s.Require().NoError(err)
	s.harness = new(taskFlowHarness)
	s.service = factory.NewService(filepath.Join(root, "tasks"), factory.Dependencies{
		Store: s.db, Config: cfg, ConfigPath: filepath.Join(configRoot, "config.yaml"),
		Harnesses: harness.Registry{"pi": s.harness}, Git: factorygit.OSRunner{}, Sandbox: sandbox.Git{Runner: factorygit.OSRunner{}},
	})
	handler, err := New(s.db, s.service, cfg, nil, nil, []string{"pi"}, nil, newTestAccess())
	s.Require().NoError(err)
	s.server = httptest.NewServer(handler)
	s.client = &http.Client{Timeout: 2 * time.Second}
}

func (s *taskFlowSuite) TearDownSuite() {
	if s.server != nil {
		s.server.Close()
	}
	if s.service != nil {
		s.service.Shutdown(context.Background())
	}
	if s.db != nil {
		s.Require().NoError(s.db.Close())
	}
}

func (s *taskFlowSuite) TestCreateApproveBuildAndCheck() {
	var created taskResponse
	s.request(http.MethodPost, "/api/v1/tasks", map[string]any{
		"request":    flowTaskRequest,
		"repository": map[string]any{"type": "local", "path": s.repo},
	}, http.StatusCreated, &created)
	s.Require().NotEmpty(created.ID)

	planned := s.awaitState(created.ID, string(factory.AwaitingApproval))
	s.Require().NotEmpty(planned.PlanDigest)
	s.Contains(planned.AvailableActions, "approve")

	var planningAttempts []store.Phase
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/attempts", nil, http.StatusOK, &planningAttempts)
	s.Equal([]string{"prepare", "planning"}, phaseNames(planningAttempts))
	for _, attempt := range planningAttempts {
		s.Equal("success", attempt.Status)
	}

	var planningResults []store.Envelope
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/results", nil, http.StatusOK, &planningResults)
	s.Require().Len(planningResults, 1)
	s.Equal("planner", planningResults[0].AgentRole)
	s.True(planningResults[0].Valid)
	s.JSONEq(flowPlan, planningResults[0].Payload)

	s.request(http.MethodPost, "/api/v1/tasks/"+created.ID+"/approve", map[string]string{"plan_digest": planned.PlanDigest}, http.StatusAccepted, nil)
	completed := s.awaitState(created.ID, string(factory.Completed))
	s.Equal("flow-e2e", completed.ApprovalActor)
	s.NotEmpty(completed.ApprovalAt)

	var attempts []store.Phase
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/attempts", nil, http.StatusOK, &attempts)
	s.Equal([]string{"prepare", "planning", "build", "checks"}, phaseNames(attempts))
	for _, attempt := range attempts {
		s.Equal("success", attempt.Status)
	}

	var checks []store.Check
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/checks", nil, http.StatusOK, &checks)
	s.Require().Len(checks, 1)
	s.Equal("build-output", checks[0].Name)
	s.Equal("passed", checks[0].Status)
	s.Equal("checks", checks[0].StageID)
	s.Equal(0, checks[0].ExitCode)

	var results []store.Envelope
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/results", nil, http.StatusOK, &results)
	s.Require().Len(results, 2)
	s.Equal("build", results[1].OutputType)
	s.JSONEq(flowBuild, results[1].Payload)
	s.Len(s.harness.Requests(), 2)
}

func (s *taskFlowSuite) awaitState(taskID, expected string) taskResponse {
	s.T().Helper()
	deadline := time.Now().Add(10 * time.Second)
	var task taskResponse
	for time.Now().Before(deadline) {
		s.request(http.MethodGet, "/api/v1/tasks/"+taskID, nil, http.StatusOK, &task)
		if task.State == expected {
			return task
		}
		if task.State == string(factory.Blocked) || task.State == string(factory.Aborted) {
			s.T().Fatalf("task reached %s while awaiting %s: %s", task.State, expected, task.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.T().Fatalf("task state = %s, want %s: %s", task.State, expected, task.Error)
	return taskResponse{}
}

func (s *taskFlowSuite) request(method, path string, body any, wantStatus int, target any) {
	s.T().Helper()
	var payload bytes.Buffer
	if body != nil {
		s.Require().NoError(json.NewEncoder(&payload).Encode(body))
	}
	request, err := http.NewRequest(method, s.server.URL+path, &payload)
	s.Require().NoError(err)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Software-Factory-Actor", "flow-e2e")
	response, err := s.client.Do(request)
	s.Require().NoError(err)
	defer response.Body.Close()
	s.Require().Equal(wantStatus, response.StatusCode)
	if target != nil {
		s.Require().NoError(json.NewDecoder(response.Body).Decode(target))
	}
}

func (s *taskFlowSuite) git(args ...string) {
	s.T().Helper()
	command := exec.Command("git", append([]string{"-C", s.repo}, args...)...)
	output, err := command.CombinedOutput()
	s.Require().NoError(err, string(output))
}

func (s *taskFlowSuite) writePrompt(root, role, name, content string) {
	s.T().Helper()
	directory := filepath.Join(root, "prompts", role)
	s.Require().NoError(os.MkdirAll(directory, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600))
}

func phaseNames(phases []store.Phase) []string {
	names := make([]string, len(phases))
	for index, phase := range phases {
		names[index] = phase.Name
	}
	return names
}
