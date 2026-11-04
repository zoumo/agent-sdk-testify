package mock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
	"github.com/zoumo/agent-sdk-testify/protocol"
)

func mockSession(ctx context.Context, o Options) (err error) {
	if o.Timeout < 0 {
		return exitcode.Fail(exitcode.Configuration, errors.New("mock timeout must be nonnegative"))
	}
	if o.ProgressFD != -1 && o.ProgressFD < 3 {
		return exitcode.Fail(exitcode.Configuration, errors.New("progress-fd must be >=3"))
	}
	if o.Connection == "" {
		o.Connection = "main"
	}
	session, err := lifecycle.OpenSession(o.SessionDir)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	if err := lifecycle.SessionOpen(o.SessionDir); err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	if err := protocol.ValidateNativeArgs(session.AgentType, o.NativeArgs); err != nil {
		return bad(err)
	}

	artifact, err := lifecycle.LatestArtifact(o.SessionDir, session, false)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	if !slices.Contains(artifact.Connections, o.Connection) {
		return bad(fmt.Errorf("undeclared mock connection %s", o.Connection))
	}

	for _, connection := range artifact.Connections {
		if _, _, err := loadMockPhase(artifact.Path, session.AgentType, connection); err != nil {
			return exitcode.Fail(exitcode.Configuration, err)
		}
	}
	run, err := lifecycle.BeginMock(o.SessionDir, o.Connection, o.NativeArgs, o.Timeout, o.ProgressFD, artifact)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	defer func() { err = errors.Join(err, run.Close()) }()
	if err := os.Chdir(run.Execution.Workspace); err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}

	connectionPath, processPath, err := mockConnectionPaths(run, o.Connection)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
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
		process["exitCode"] = exitcode.Code(err)
		if err == nil {
			process["status"] = "passed"
			status = "passed"
		} else {
			process["error"] = err.Error()
		}
		processErr := tool.WriteJSON(processPath, process)
		indexErr := lifecycle.CompleteMockConnection(run, status)
		err = errors.Join(err, processErr, indexErr)
	}()

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
