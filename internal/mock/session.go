package mock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
)

func mockSession(ctx context.Context, o Options, input, output *os.File) (err error) {
	session, err := lifecycle.OpenSession(o.SessionDir)
	if err != nil {
		return err
	}
	if err := protocol.ValidateNativeArgs(session.AgentType, o.NativeArgs); err != nil {
		return err
	}

	artifact, err := lifecycle.LatestArtifact(o.SessionDir, session)
	if err != nil {
		return err
	}
	if !slices.Contains(artifact.Connections, o.Connection) {
		return fmt.Errorf("undeclared mock connection %s", o.Connection)
	}

	for _, connection := range artifact.Connections {
		if _, _, err := loadMockPhase(artifact.Path, session.AgentType, connection); err != nil {
			return err
		}
	}
	return lifecycle.RunMock(ctx, lifecycle.MockRequest{
		SessionDir: o.SessionDir,
		Connection: o.Connection,
		NativeArgs: o.NativeArgs,
		Timeout:    o.Timeout,
		ProgressFD: o.ProgressFD,
		ResultFD:   o.ResultFD,
		Artifact:   *artifact,
	}, mockReplayPolicy{options: o, agent: session.AgentType, input: input, output: output})
}

type mockReplayPolicy struct {
	options Options
	agent   string
	input   *os.File
	output  *os.File
}

func (p mockReplayPolicy) Replay(ctx context.Context, run lifecycle.MockContext) error {
	if err := os.Chdir(run.Workspace()); err != nil {
		return err
	}
	execution := run.Execution()
	err := replayConnection(ctx, p.options, p.input, p.output, run.Artifact().Path, p.agent, run.Connection(), run.ConnectionReceipt())
	return errors.Join(err, history.Check(run.OverlayRoot(), execution.HistoryFiles))
}
