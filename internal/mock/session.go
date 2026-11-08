package mock

import (
	"context"
	"errors"
	"os"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
)

// ValidateMaterial verifies every declared replay phase before an execution is
// allocated.
func ValidateMaterial(material corpus.Material, agent string) error {
	for _, connection := range material.Connections {
		if _, _, err := loadMockPhase(material.Path, agent, connection); err != nil {
			return err
		}
	}
	return nil
}

type mockReplayPolicy struct {
	prepared Prepared
	input    *os.File
	output   *os.File
}

func (p mockReplayPolicy) Replay(ctx context.Context, run lifecycle.MockContext) error {
	execution := run.Execution()
	// Every replay and history path is rooted in the session or its execution
	// overlay. Avoid process-wide chdir so concurrent library calls stay isolated.
	err := replayConnection(ctx, p.prepared, p.input, p.output, run.Artifact().Path, run.Connection(), run.ConnectionReceipt())
	return errors.Join(err, history.Check(run.OverlayRoot(), execution.HistoryFiles))
}
