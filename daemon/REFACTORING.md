# Refactoring golang code base


## Moduling and abstraction
 - Currently factory part contains Allocate Task Workspace
  - This part should be moved it is own module. And we should be able to work with Git Branches, Git Worktrees, Docker containers and we should be flexibale on working with different workspaces

- Factory->>Harness: Run stage agent 
  - This part also compicated we can abstract Runners and create different runners for each state Like Planner, Builder, Reviewer
  - See this code base how they did executor with it is locks and removed locks usage from everywhere like we do https://github.com/SourceCode/docker/blob/master/daemon/exec/exec.go

- Events currently all the states inserts events directly into db.Events instead we should create EventStore service and factory should send events 
- Entiry code base should be simplified into minimum code and removed most of the part, so factory should do Orchestration through runners, that runs abstracted Planner, Builder, Reviewer and etc based on configs that we provided.
- After that runners should hand over envelop into next state through the orchestrator. 
- And harness should provide artificats/results in MARKDOWN so we should store them and rendere them properly to user in UI.  

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
