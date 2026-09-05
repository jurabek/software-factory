# Software Factory

The Software Factory coordinates bounded, evidence-backed repository changes inside durable Task Workspaces.

## Language

**Task**:
The durable statement of a requested outcome together with its execution history, repository inputs, checks, and risk constraints.
_Avoid_: Campaign, Feature Request, job, workflow

**Task Workspace**:
The private filesystem root owned by one Task. It contains one or more repository materializations, attempts, snapshots, sessions, and artifacts.
_Avoid_: Campaign directory, checkout

**Repository Materialization**:
An isolated clone or worktree of one repository inside a Task Workspace.
_Avoid_: Repository Context, working copy

**Attempt**:
One immutable execution of a Task phase against recorded workspace inputs.
_Avoid_: Retry, run, session

**Task transition policy**:
The deterministic rules that decide how a Task advances or branches after an observed outcome or Intervention.
_Avoid_: Campaign transition policy, routing logic, state switch

**Intervention**:
A persisted human message anchored to an Event, Artifact, or Attempt that may comment, steer, retry, revise, or repair work.
_Avoid_: Feedback, instruction override
