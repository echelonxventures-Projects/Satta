package orchestration_test

import (
	"testing"

	"github.com/satta/platform/libs/go/orchestration"
	"github.com/satta/platform/libs/go/orchestration/orchestrationtest"
)

// TestLocalRunnerContract: the in-process binding must satisfy the same
// saga contract as the durable engine, including compensation ordering and
// the explicit refusal to end in a partially-applied state.
func TestLocalRunnerContract(t *testing.T) {
	orchestrationtest.RunContractTests(t, func(t *testing.T) orchestration.Runner {
		t.Helper()
		return orchestration.NewLocalRunner(nil)
	})
}
