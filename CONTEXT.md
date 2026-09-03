# Software Factory

The Software Factory coordinates bounded, evidence-backed repository changes from an approved request through implementation completion.

## Language

**Feature Request**:
The versioned, approval-bearing statement of a requested outcome, its local work, checks, and risk constraints.
_Avoid_: Ticket, task, prompt

**Campaign**:
A tracked execution of a Feature Request through planning, building, independent review and required-check execution, and implementation completion.
_Avoid_: Run, job, workflow

**Campaign transition policy**:
The deterministic rules that decide how a Campaign advances from its current state after an observed outcome.
_Avoid_: Routing logic, state switch

**Repository Context**:
The Campaign-pinned facts from a target repository and its Software Factory block, including checks, protected output, source-control identity, and effective risk signals.
_Avoid_: Domain Profile, repository configuration
