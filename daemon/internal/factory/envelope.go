package factory

import (
	"github.com/jurabek/software-factory/daemon/internal/agentexec"
	"github.com/jurabek/software-factory/daemon/internal/builder"
	"github.com/jurabek/software-factory/daemon/internal/planner"
	"github.com/jurabek/software-factory/daemon/internal/reviewer"
)

type Common = agentexec.Common
type PlanStep = planner.PlanStep
type Plan = planner.Result
type TestChange = builder.TestChange
type Build = builder.Result
type Finding = reviewer.Finding
type Review = reviewer.Result

func ValidatePlan(text string) (Plan, error)     { return planner.Validate(text) }
func ValidateBuild(text string) (Build, error)   { return builder.Validate(text) }
func ValidateReview(text string) (Review, error) { return reviewer.Validate(text) }
func envelopeInstructions(role string) string {
	switch role {
	case "planner":
		return planner.Instructions()
	case "builder", "build":
		return builder.Instructions()
	case "reviewer", "review":
		return reviewer.Instructions()
	default:
		return "Return exactly one JSON object with every required field and no Markdown."
	}
}
