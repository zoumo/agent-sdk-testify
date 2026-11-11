package mock

import (
	"context"
	"os"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
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
	// Every replay and history path is rooted in the fixture or its execution
	// overlay. Avoid process-wide chdir so concurrent library calls stay isolated.
	return replayConnection(ctx, p.prepared, run, p.input, p.output)
}
