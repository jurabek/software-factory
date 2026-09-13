# Refactoring golang code base


## Modules and abstraction
 - Currently factory part contains Allocate Task Workspace
  - This part should be moved into its own module. We should be able to work with Git branches, Git worktrees, Docker containers, and different workspace implementations.

- Factory->>Harness: Run stage agent 
  - This part is also complicated. We can abstract runners and create different runners for each state, such as Planner, Builder, and Reviewer.
  - See how this codebase implemented an executor with its locks and removed lock usage elsewhere: https://github.com/SourceCode/docker/blob/master/daemon/exec/exec.go

- Events currently all the states inserts events directly into db.Events instead we should create EventStore service and factory should send events 
- The entire codebase should be simplified so the factory orchestrates runners for configured Planner, Builder, Reviewer, and other stages.
- After that runners should hand over envelop into next state through the orchestrator. 
- The harness should provide artifacts and results in Markdown so we can store and render them properly in the UI.

## Current Task Execution

```mermaid
sequenceDiagram
    actor User
    participant Browser
    participant App as Next.js application
    participant Registry as Daemon registry
    participant API as Daemon tasks handler
    participant Factory
    participant Store as SQLite store
    participant Git
    participant Harness as Agent harness

    User->>Browser: Submit Task
    Browser->>App: POST /api/daemons/{daemonId}/tasks
    App->>Registry: Resolve daemon and credential
    Registry->>API: POST /api/v1/tasks
    API->>Factory: Create(request)
    Factory->>Factory: Allocate Task Workspace
    Factory->>Store: Persist preparing Task and select branch
    Store-->>Factory: Task
    Factory-->>API: Launch background execution
    Factory-->>API: Task
    API-->>Browser: 201 Created

    Factory->>Git: Materialize repositories
    Git-->>Factory: Repository profiles and base SHAs
    Factory->>Store: Persist preparation phase and repository state
    Factory->>Harness: Run Planner
    Harness-->>Factory: Validated plan envelope
    Factory->>Store: Persist plan, events, awaiting approval

    Browser->>App: Open events stream
    App->>API: GET /api/v1/tasks/{id}/events/stream
    API->>Store: Poll events after cursor
    Store-->>API: New events
    API-->>App: SSE events
    App-->>Browser: SSE events

    User->>Browser: Approve current plan digest
    Browser->>App: POST .../tasks/{taskId}/approve
    App->>Registry: Resolve daemon and credential
    Registry->>API: POST /api/v1/tasks/{id}/approve
    API->>Factory: Approve(id, actor, digest)
    Factory->>Store: Persist approval and transition Task
    Factory-->>API: Launch background execution
    API-->>Browser: 202 Accepted

    loop Each configured pipeline stage
        alt Build or review stage
            Factory->>Harness: Run stage agent
            Harness-->>Factory: Validated result envelope
        else Verify stage
            Factory->>Git: Run deterministic checks and comparisons
            Git-->>Factory: Check evidence
        end
        Factory->>Store: Persist attempt, evidence, and events
    end

    alt All stages succeed
        Factory->>Store: Transition Task to completed
    else Stage fails
        Factory->>Store: Transition Task to blocked
    end

    Browser->>App: Read latest Task state and events
    App->>API: GET Task and events
    API->>Store: Read state and evidence
    Store-->>API: Task and events
    API-->>App: Current result
    App-->>Browser: Render completed or blocked state
```
