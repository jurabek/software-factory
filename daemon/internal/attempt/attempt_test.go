package attempt

import (
	"context"
	"testing"

	"github.com/jurabek/software-factory/daemon/internal/store"
)

type fakePhases []store.Phase

func (f fakePhases) List(context.Context, string) ([]store.Phase, error) { return f, nil }

func (f fakePhases) ByID(_ context.Context, _ string, id string) (store.Phase, error) {
	for _, phase := range f {
		if phase.ID == id {
			return phase, nil
		}
	}
	return store.Phase{}, store.ErrNotFound
}

func TestResumeRequiresCurrentSuccessAfterUpstream(t *testing.T) {
	ctx := context.Background()
	ledger := NewLedger(fakePhases{
		{ID: "plan-1", Name: "plan", Sequence: 1, Status: "success"},
		{ID: "build-1", Name: "build", Sequence: 2, Status: "success"},
		{ID: "plan-2", Name: "plan", Sequence: 3, Status: "success"},
		{ID: "build-2", Name: "build", Sequence: 4, Status: "failed"},
	})
	if _, ok, err := ledger.Resume(ctx, "task", "build", "plan-2"); err != nil || ok {
		t.Fatalf("failed latest build resumed: ok=%v err=%v", ok, err)
	}
	if err := ledger.Require(ctx, "task", "plan", "plan-1"); err == nil {
		t.Fatal("superseded plan attempt accepted")
	}
	if err := ledger.Require(ctx, "task", "plan", "plan-2"); err != nil {
		t.Fatal(err)
	}
	if phase, ok, err := ledger.Resume(ctx, "task", "plan", ""); err != nil || !ok || phase.ID != "plan-2" {
		t.Fatalf("plan resume = %v %v %v", phase.ID, ok, err)
	}
}

func TestResumeRejectsAttemptOlderThanUpstream(t *testing.T) {
	ledger := NewLedger(fakePhases{
		{ID: "build-1", Name: "build", Sequence: 2, Status: "success"},
		{ID: "plan-2", Name: "plan", Sequence: 3, Status: "success"},
	})
	if _, ok, err := ledger.Resume(context.Background(), "task", "build", "plan-2"); err != nil || ok {
		t.Fatalf("stale build resumed: ok=%v err=%v", ok, err)
	}
}

func TestEligibleAfter(t *testing.T) {
	if !EligibleAfter(3, 2) || EligibleAfter(2, 3) || EligibleAfter(2, 2) {
		t.Fatal("EligibleAfter must require a strictly later sequence")
	}
}

func TestLocksSharePerTaskMutex(t *testing.T) {
	locks := &Locks{}
	if locks.For("a") != locks.For("a") || locks.For("a") == locks.For("b") {
		t.Fatal("locks must be stable per task and distinct across tasks")
	}
}
