package harness

import (
	"context"
	"errors"

	"github.com/jurabek/software-factory/daemon/internal/session"
)

var ErrProcessTerminationUnconfirmed = errors.New("process group termination was not confirmed")

type Model struct {
	Provider, ID  string
	ContextWindow int
	Thinking      []string
}
type Request struct {
	CWD,
	Prompt,
	SystemPrompt,
	Model,
	Thinking,
	SessionID,
	SessionDirectory,
	RawOutputPath string
	DeadlineMS            int
	Resume                bool
	AdditionalDirectories []string
}
type Event = session.Entry

type (
	EventSink func(context.Context, Event) error

	Cost struct {
		Input,
		Output,
		CacheRead,
		CacheWrite,
		Reasoning,
		Total float64
	}

	Usage struct {
		Input,
		Output,
		CacheRead,
		CacheWrite,
		Reasoning,
		TotalTokens int
		Cost  float64
		Costs Cost
	}
)

type Result struct {
	Text                         string
	ExitCode                     int
	SessionID, Provider, Model   string
	Usage                        Usage
	ContextTokens, ContextWindow int
	SessionReady                 bool
	AccountingComplete           bool
	NativeTranscriptPath         string
}
type Harness interface {
	Models(context.Context) ([]Model, error)
	Run(context.Context, Request, EventSink) (Result, error)
}
type Registry map[string]Harness

func (r Registry) Get(name string) (Harness, bool) { h, ok := r[name]; return h, ok }
