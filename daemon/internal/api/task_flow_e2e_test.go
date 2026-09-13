package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/builder"
	"github.com/jurabek/software-factory/daemon/internal/config"
	"github.com/jurabek/software-factory/daemon/internal/creation"
	factorygit "github.com/jurabek/software-factory/daemon/internal/git"
	"github.com/jurabek/software-factory/daemon/internal/harness"
	"github.com/jurabek/software-factory/daemon/internal/intervention"
	"github.com/jurabek/software-factory/daemon/internal/messaging"
	"github.com/jurabek/software-factory/daemon/internal/orchestrator"
	"github.com/jurabek/software-factory/daemon/internal/pipeline"
	"github.com/jurabek/software-factory/daemon/internal/planner"
	"github.com/jurabek/software-factory/daemon/internal/projection"
	"github.com/jurabek/software-factory/daemon/internal/reviewer"
	"github.com/jurabek/software-factory/daemon/internal/sandbox"
	"github.com/jurabek/software-factory/daemon/internal/session"
	"github.com/jurabek/software-factory/daemon/internal/stagekit"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/task"
	"github.com/jurabek/software-factory/daemon/internal/verifier"
	"github.com/jurabek/software-factory/daemon/internal/workspace"
	"github.com/stretchr/testify/suite"
)

const (
	flowTaskRequest = "Create the build artifact"
	flowPlan        = `{"status":"success","summary":"Create the requested artifact","artifacts":[],"notes_for_next_agent":"","report_markdown":"# Plan\n\nCreate built.txt.","steps":[{"id":"build-artifact","description":"Create built.txt","expected_files":["built.txt"],"acceptance_criteria":["deterministic check passes"]}],"questions":[]}`
	flowBuild       = `{"status":"success","summary":"Created the build artifact","artifacts":[],"notes_for_next_agent":"","report_markdown":"# Build\n\nCreated built.txt.","changed_files":["built.txt"],"commit_message":"Create build artifact","test_changes":[]}`
	flowReview      = `{"status":"success","summary":"Implementation satisfies the plan","artifacts":[],"notes_for_next_agent":"","report_markdown":"# Review\n\nApproved.","approved":true,"findings":[],"blocking":[]}`
	flowMessage     = "Keep the public API stable."
)

type taskFlowHarness struct {
	mu           sync.Mutex
	requests     []harness.Request
	buildStarted chan struct{}
	releaseBuild chan struct{}
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
		close(h.buildStarted)
		select {
		case <-h.releaseBuild:
		case <-time.After(2 * time.Second):
			return result, fmt.Errorf("build release timed out")
		}
		result.Text = flowBuild
	case 3:
		if !request.Resume || request.Prompt != flowMessage {
			return result, fmt.Errorf("message continuation = %+v", request)
		}
		result.Text = flowBuild
	case 4:
		if !strings.Contains(request.SystemPrompt, "Review the implementation") || !strings.Contains(request.Prompt, "Create the requested artifact") {
			return result, fmt.Errorf("reviewer prompt missing evidence handoff: system=%q user=%q", request.SystemPrompt, request.Prompt)
		}
		result.Text = flowReview
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
	service *orchestrator.Service
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
	s.writePrompt(configRoot, "reviewer", "system.md", "Review the implementation without editing files.")
	s.writePrompt(configRoot, "reviewer", "user.md", "Task {{.TaskID}}: {{.Request}}\nPlan: {{.Plan}}\nChecks: {{.Checks}}")

	cfg := config.Config{
		Defaults: config.Defaults{CodingAgent: "pi", Model: "test/model", Thinking: "low"},
		Runtime:  config.Runtime{AgentDeadlineMS: 5_000},
		Agents: []config.Agent{
			{Name: "planner", Model: "test/model", Thinking: "low", PromptEngineering: config.PromptEngineering{System: "prompts/planner/system.md", User: "prompts/planner/user.md"}},
			{Name: "builder", Model: "test/model", Thinking: "low", PromptEngineering: config.PromptEngineering{System: "prompts/builder/system.md", User: "prompts/builder/user.md"}},
			{Name: "reviewer", Model: "test/model", Thinking: "low", PromptEngineering: config.PromptEngineering{System: "prompts/reviewer/system.md", User: "prompts/reviewer/user.md"}},
		},
		Pipelines: []config.Pipeline{{Name: "standard", Default: true, Stages: []config.Stage{{ID: "plan", Kind: "plan", Agent: "planner"}, {ID: "build", Kind: "build", Agent: "builder"}, {ID: "checks", Kind: "verify"}, {ID: "review", Kind: "review", Agent: "reviewer"}}}},
	}
	var err error
	s.db, err = store.Open(filepath.Join(root, "factory.db"))
	s.Require().NoError(err)
	s.harness = &taskFlowHarness{buildStarted: make(chan struct{}), releaseBuild: make(chan struct{})}
	taskRoot := filepath.Join(root, "tasks")
	configPath := filepath.Join(configRoot, "config.yaml")
	registry := harness.Registry{"pi": s.harness}
	sandboxRunner := sandbox.Git{Runner: factorygit.OSRunner{}}
	kit := stagekit.New(s.db, factorygit.OSRunner{}, registry, sandboxRunner, cfg, configPath, taskRoot)
	events := orchestrator.NewEvents(s.db)
	taskService := task.New(taskRoot, task.Deps{Store: s.db, Config: cfg, ConfigPath: configPath, Harnesses: registry, Git: factorygit.OSRunner{}, Sandbox: sandboxRunner})
	creationStage := creation.New(taskService, kit, events)
	plannerStage := planner.New(kit, events)
	s.service = orchestrator.New(taskRoot, orchestrator.Dependencies{
		Store: s.db, Events: events, Workflow: pipeline.New(
			plannerStage,
			builder.New(kit),
			verifier.New(kit),
			reviewer.New(kit),
			creationStage,
		),
	})
	interventions := intervention.New(intervention.Deps{Store: s.db, Git: factorygit.OSRunner{}, Snapshots: workspace.New(s.db, factorygit.OSRunner{}), Config: cfg, ConfigPath: configPath, Root: taskRoot, Events: events})
	messages := messaging.New(messaging.Deps{Store: s.db, Config: cfg, ConfigPath: configPath, Harnesses: registry, Root: taskRoot, Interventions: interventions, Events: events})
	handler, err := New(s.db, Communicators{Creator: creationStage, Events: events, Planner: plannerStage, Messages: messages, Intervention: interventions, Projection: projection.New(projection.Deps{Store: s.db, Config: cfg, ConfigPath: configPath}), Tasks: taskService}, cfg, nil, nil, []string{"pi"}, nil, newTestAccess())
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

	planned := s.awaitState(created.ID, string(orchestrator.AwaitingApproval))
	s.Require().NotEmpty(planned.PlanDigest)
	s.Contains(planned.AvailableActions, "approve")

	var planningAttempts []store.Phase
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/attempts", nil, http.StatusOK, &planningAttempts)
	s.Equal([]string{"creation", "plan"}, phaseNames(planningAttempts))
	for _, attempt := range planningAttempts {
		s.Equal("success", attempt.Status)
	}

	var planningResults []store.Envelope
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/results", nil, http.StatusOK, &planningResults)
	s.Require().Len(planningResults, 1)
	s.Equal("plan", planningResults[0].AgentRole)
	s.True(planningResults[0].Valid)
	s.JSONEq(flowPlan, planningResults[0].Payload)

	s.request(http.MethodPost, "/api/v1/tasks/"+created.ID+"/approve", map[string]string{"plan_digest": planned.PlanDigest}, http.StatusAccepted, nil)
	select {
	case <-s.harness.buildStarted:
	case <-time.After(2 * time.Second):
		s.T().Fatal("build did not start")
	}
	var beforeMessage struct {
		Events []store.Event `json:"events"`
		Cursor int64         `json:"cursor"`
	}
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/events?tail=1", nil, http.StatusOK, &beforeMessage)
	var accepted, duplicate store.Message
	messageBody := map[string]string{"text": flowMessage, "idempotency_key": "flow-message"}
	s.request(http.MethodPost, "/api/v1/tasks/"+created.ID+"/messages", messageBody, http.StatusAccepted, &accepted)
	s.request(http.MethodPost, "/api/v1/tasks/"+created.ID+"/messages", messageBody, http.StatusAccepted, &duplicate)
	s.Equal(accepted.ID, duplicate.ID)
	queued, queuedStream := s.readStreamEvent(created.ID, beforeMessage.Cursor, 0)
	s.Equal(session.KindTaskMessage, queued.Kind)
	s.Equal("queued", messageDeliveryStatus(queued))
	queuedStream.Close()
	close(s.harness.releaseBuild)
	completed := s.awaitState(created.ID, string(orchestrator.Completed))
	s.Equal("flow-e2e", completed.ApprovalActor)
	s.NotEmpty(completed.ApprovalAt)
	delivered, deliveredStream := s.readStreamEvent(created.ID, 0, queued.Sequence)
	defer deliveredStream.Close()
	for delivered.Kind != session.KindTaskMessage || messageDeliveryStatus(delivered) != "delivered" {
		delivered = readSSEEvent(s.T(), deliveredStream.reader)
	}
	s.Greater(delivered.Sequence, queued.Sequence)
	var messages []store.Message
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/messages", nil, http.StatusOK, &messages)
	s.Require().Len(messages, 1)
	s.Equal("delivered", messages[0].DeliveryStatus)

	var attempts []store.Phase
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/attempts", nil, http.StatusOK, &attempts)
	s.Equal([]string{"creation", "plan", "build", "checks", "review"}, phaseNames(attempts))
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
	s.Require().Len(results, 4)
	for _, result := range results[1:3] {
		s.Equal("build", result.OutputType)
		s.JSONEq(flowBuild, result.Payload)
	}
	s.Equal("review", results[3].OutputType)
	s.JSONEq(flowReview, results[3].Payload)

	var history struct {
		Events []store.Event `json:"events"`
		Cursor int64         `json:"cursor"`
	}
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/events?limit=100", nil, http.StatusOK, &history)
	s.Require().NotEmpty(history.Events)
	var approvalEvent *store.Event
	for index := range history.Events {
		if history.Events[index].Name == "task_approved" {
			approvalEvent = &history.Events[index]
			break
		}
	}
	s.Require().NotNil(approvalEvent)
	s.Equal("task_approved", approvalEvent.Name)
	s.Equal(history.Events[len(history.Events)-1].Sequence, history.Cursor)
	for index := 1; index < len(history.Events); index++ {
		s.Greater(history.Events[index].Sequence, history.Events[index-1].Sequence)
	}
	var replay struct {
		Events []store.Event `json:"events"`
		Cursor int64         `json:"cursor"`
	}
	s.request(http.MethodGet, fmt.Sprintf("/api/v1/tasks/%s/events?after=%d", created.ID, history.Events[0].Sequence), nil, http.StatusOK, &replay)
	s.Require().NotEmpty(replay.Events)
	s.Greater(replay.Events[0].Sequence, history.Events[0].Sequence)
	s.Equal(history.Events[len(history.Events)-1].Sequence, replay.Cursor)
	var artifacts []store.Artifact
	s.request(http.MethodGet, "/api/v1/tasks/"+created.ID+"/artifacts", nil, http.StatusOK, &artifacts)
	s.Require().Len(artifacts, 4)
	for _, artifact := range artifacts {
		body := s.requestText("/api/v1/tasks/"+created.ID+"/artifacts/"+artifact.ID, http.StatusOK)
		s.NotEmpty(body)
		s.Contains(body, "#")
	}
	s.Len(s.harness.Requests(), 4)
}

type taskEventStream struct {
	reader   *bufio.Reader
	response *http.Response
	cancel   context.CancelFunc
}

func (s *taskFlowSuite) readStreamEvent(taskID string, after, lastEventID int64) (store.Event, *taskEventStream) {
	s.T().Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/tasks/%s/events/stream?after=%d", s.server.URL, taskID, after), nil)
	s.Require().NoError(err)
	request.Header.Set("Authorization", "Bearer "+testToken)
	if lastEventID > 0 {
		request.Header.Set("Last-Event-ID", strconv.FormatInt(lastEventID, 10))
	}
	response, err := s.client.Do(request)
	s.Require().NoError(err)
	s.Require().Equal(http.StatusOK, response.StatusCode)
	stream := &taskEventStream{reader: bufio.NewReader(response.Body), response: response, cancel: cancel}
	return readSSEEvent(s.T(), stream.reader), stream
}

func (s *taskEventStream) Close() {
	s.cancel()
	_ = s.response.Body.Close()
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) store.Event {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event store.Event
		if err = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &event); err != nil {
			t.Fatal(err)
		}
		return event
	}
}

func messageDeliveryStatus(event store.Event) string {
	payload, _ := event.Payload.(map[string]any)
	status, _ := payload["delivery_status"].(string)
	return status
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
		if task.State == string(orchestrator.Blocked) || task.State == string(orchestrator.Aborted) {
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

func (s *taskFlowSuite) requestText(path string, wantStatus int) string {
	s.T().Helper()
	request, err := http.NewRequest(http.MethodGet, s.server.URL+path, nil)
	s.Require().NoError(err)
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err := s.client.Do(request)
	s.Require().NoError(err)
	defer response.Body.Close()
	s.Require().Equal(wantStatus, response.StatusCode)
	content, err := io.ReadAll(response.Body)
	s.Require().NoError(err)
	return string(content)
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
