package checkrun

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunClassifiesOutcomes(t *testing.T) {
	cases := []struct {
		command string
		status  string
		exit    int
	}{
		{"echo ok", Passed, 0},
		{"exit 3", Failed, 3},
		{"definitely-not-a-command", Inconclusive, 127},
	}
	for _, c := range cases {
		result, err := Run(context.Background(), Check{Command: c.command, Dir: t.TempDir()}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != c.status || result.ExitCode != c.exit {
			t.Fatalf("%q: status=%s exit=%d, want %s %d", c.command, result.Status, result.ExitCode, c.status, c.exit)
		}
	}
}

func TestRunCancellationTerminatesProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := Run(ctx, Check{Command: "sleep 30 & sleep 30", Dir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != Cancelled {
		t.Fatalf("status = %s, want cancelled", result.Status)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("process group was not terminated")
	}
}

func TestRunOnStartFailureStopsProcess(t *testing.T) {
	want := errors.New("record failed")
	result, err := Run(context.Background(), Check{Command: "sleep 30", Dir: t.TempDir()}, func(int) error { return want })
	if !errors.Is(err, want) || result.PID == 0 {
		t.Fatalf("err = %v, pid = %d", err, result.PID)
	}
}

func TestTailCaptureKeepsLastBytes(t *testing.T) {
	capture := &tailCapture{limit: 4}
	_, _ = capture.Write([]byte("abcdef"))
	if capture.String() != "cdef" {
		t.Fatalf("tail = %q", capture.String())
	}
}
