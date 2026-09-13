package config

import "testing"

func TestValidatePipelineRequiresFixedFlow(t *testing.T) {
	agents := map[string]bool{"planner": true, "builder": true, "reviewer": true}
	valid := Pipeline{Name: "standard", Stages: []Stage{
		{ID: "plan", Kind: "plan", Agent: "planner"},
		{ID: "build", Kind: "build", Agent: "builder"},
		{ID: "check", Kind: "verify"},
		{ID: "review", Kind: "review", Agent: "reviewer"},
	}}
	if problems := validatePipeline(valid, agents); len(problems) != 0 {
		t.Fatalf("valid fixed pipeline: %v", problems)
	}

	invalid := valid
	invalid.Stages = []Stage{valid.Stages[1], valid.Stages[0], valid.Stages[2], valid.Stages[3]}
	if problems := validatePipeline(invalid, agents); len(problems) == 0 {
		t.Fatal("reordered pipeline accepted")
	}

	invalid = valid
	invalid.Stages = invalid.Stages[:3]
	if problems := validatePipeline(invalid, agents); len(problems) == 0 {
		t.Fatal("pipeline without review accepted")
	}
}

func TestUpgradeLegacyPipelineAddsPlanStage(t *testing.T) {
	configured := upgradeLegacyPipelines(Config{
		Agents: []Agent{{Name: "planner"}, {Name: "builder"}, {Name: "reviewer"}},
		Pipelines: []Pipeline{{Name: "standard", Stages: []Stage{
			{ID: "build", Kind: "build", Agent: "builder"},
			{ID: "check", Kind: "verify"},
			{ID: "review", Kind: "review", Agent: "reviewer"},
		}}},
	})
	stages := configured.Pipelines[0].Stages
	if len(stages) != 4 || stages[0] != (Stage{ID: "plan", Kind: "plan", Agent: "planner"}) {
		t.Fatalf("upgraded stages = %+v", stages)
	}
}
