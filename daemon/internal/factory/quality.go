package factory

import (
	"context"

	"github.com/jurabek/software-factory/daemon/internal/builder"
	"github.com/jurabek/software-factory/daemon/internal/store"
	"github.com/jurabek/software-factory/daemon/internal/verifier"
)

func (s *qualityService) builderValidator(ctx context.Context, task store.Task, profile Materialization) validator {
	return func(text string) (any, error) {
		return builder.ValidateWithEvidence(ctx, s.git, task.RepositoryPath, reviewBase(task), profile.Tests, text)
	}
}

func (s *qualityService) persistBuilderEvidence(ctx context.Context, task store.Task, phase store.Phase, payload string) error {
	return builder.PersistEvidence(ctx, s.git, s.db, task, phase, payload)
}

func (s *qualityService) verifierService() verifier.Service {
	return verifier.New(verifier.Deps{Checks: s.db, Git: s.git, Snapshots: s.snapshots})
}

func (s *qualityService) runChecks(ctx context.Context, task store.Task, phase store.Phase, checks []Check, checkPhase, baseline string) error {
	return s.verifierService().RunChecks(ctx, task, phase, checks, checkPhase, baseline)
}

func (s *qualityService) runComparisons(ctx context.Context, task store.Task, phase store.Phase, profile Materialization) error {
	return s.verifierService().RunComparisons(ctx, task, phase, profile)
}

func reviewBase(task store.Task) string {
	if task.ReviewBaseSHA != "" {
		return task.ReviewBaseSHA
	}
	return task.BaseSHA
}
