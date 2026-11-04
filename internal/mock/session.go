package mock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

func mockSession(ctx context.Context, o Options) (err error) {
	result, err := lifecycle.ResultStream(o.ResultFD)
	if err != nil {
		return err
	}
	if result != nil {
		defer result.Close()
	}
	if o.Timeout < 0 {
		return errors.New("mock timeout must be nonnegative")
	}
	if o.ProgressFD != -1 && o.ProgressFD < 3 {
		return errors.New("progress-fd must be >=3")
	}
	if o.Connection == "" {
		o.Connection = "main"
	}
	session, err := lifecycle.OpenActionSession(o.SessionDir)
	if err != nil {
		return err
	}
	if err := protocol.ValidateNativeArgs(session.AgentType, o.NativeArgs); err != nil {
		return err
	}

	artifact, err := lifecycle.LatestArtifact(o.SessionDir, session, false)
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
	run, err := lifecycle.BeginMock(o.SessionDir, o.Connection, o.NativeArgs, o.Timeout, o.ProgressFD, artifact)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, run.Close()) }()
	if run.Execution.Limits.TimeoutNS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(run.Execution.Limits.TimeoutNS))
		defer cancel()
	}

	connectionPath, processPath, err := mockConnectionPaths(run, o.Connection)
	if err != nil {
		return err
	}
	process := tool.Object{
		"schemaVersion": 1,
		"sessionId":     session.SessionID,
		"executionId":   run.Execution.ExecutionID,
		"connectionId":  o.Connection,
		"status":        "failed",
		"exitCode":      -1,
		"cwd":           run.Execution.Workspace,
		"toolVersion":   version.Version,
	}
	if self, toolVersion, hash, selfErr := lifecycle.SelfCommand(); selfErr == nil {
		process["tool"] = self
		process["toolVersion"] = toolVersion
		process["toolSHA256"] = hash
	}
	status := "failed"
	defer func() {
		process["exitCode"] = 0
		if err == nil {
			process["status"] = "passed"
			status = "passed"
		} else {
			process["exitCode"] = 1
			process["error"] = err.Error()
		}
		processErr := tool.WriteJSON(processPath, process)
		if processErr != nil {
			status = "failed"
		}
		indexErr := lifecycle.CompleteMockConnection(run, status)
		err = errors.Join(err, processErr, indexErr)
		err = errors.Join(err, lifecycle.WriteActionResult(ctx, run, result, err))
	}()

	if err := lifecycle.NotifyActionStarted(ctx, run, result); err != nil {
		return err
	}
	if err := os.Chdir(run.Execution.Workspace); err != nil {
		return err
	}
	return replayConnection(ctx, o, artifact.Path, session.AgentType, o.Connection, connectionPath)
}

func mockConnectionPaths(run *lifecycle.ActionRun, id string) (string, string, error) {
	for _, connection := range run.Execution.Connections {
		if connection.ID == id {
			return connection.Receipt, connection.ProcessReceipt, nil
		}
	}
	return "", "", fmt.Errorf("undeclared mock connection %s", id)
}
