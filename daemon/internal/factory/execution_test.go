package factory

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecutionOwnerRejectsDuplicateTaskExecution(t *testing.T) {
	owner := newExecutionOwner()
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32

	if !owner.start("task", "first", func(context.Context) error {
		runs.Add(1)
		close(started)
		<-release
		return nil
	}, nil, nil) {
		t.Fatal("first execution was not admitted")
	}
	<-started
	if owner.start("task", "second", func(context.Context) error {
		runs.Add(1)
		return nil
	}, nil, nil) {
		t.Fatal("duplicate execution was admitted")
	}
	close(release)

	if err := owner.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("execution count = %d, want 1", got)
	}
}

func TestExecutionOwnerShutdownCancelsAndJoinsWorkers(t *testing.T) {
	owner := newExecutionOwner()
	started := make(chan struct{})
	settled := make(chan struct{})

	if !owner.start("task", "execution", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(settled)
		return ctx.Err()
	}, nil, nil) {
		t.Fatal("execution was not admitted")
	}
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if ids := owner.stopAll(); len(ids) != 1 || ids[0] != "task" {
		t.Fatalf("stopped tasks = %v", ids)
	}
	if err := owner.wait(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-settled:
	default:
		t.Fatal("shutdown returned before worker settled")
	}
}

func TestExecutionOwnerKeepsTaskOwnedUntilSettlementCompletes(t *testing.T) {
	owner := newExecutionOwner()
	started := make(chan struct{})
	releaseRun := make(chan struct{})
	settling := make(chan struct{})
	releaseSettlement := make(chan struct{})
	if !owner.start("task", "execution", func(context.Context) error {
		close(started)
		<-releaseRun
		return nil
	}, func(context.Context, error) {
		close(settling)
		<-releaseSettlement
	}, nil) {
		t.Fatal("execution was not admitted")
	}
	<-started
	close(releaseRun)
	<-settling
	stopped := make(chan error, 1)
	go func() { stopped <- owner.stopAndWait(context.Background(), "task") }()
	select {
	case err := <-stopped:
		t.Fatalf("control returned before settlement: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseSettlement)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}
