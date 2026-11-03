package mock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
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
	if o.Agent != "" && o.Agent != session.AgentType {
		return exitcode.Fail(exitcode.Configuration, errors.New("mock agent type differs from the prepared session"))
	}
	if o.Report != "" {
		return exitcode.Fail(exitcode.Configuration, errors.New("prepared mock writes connection receipts; --report is only valid for the managed alias"))
	}
	plan, err := lifecycle.LoadPlan(session)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	if versionProbe(o.NativeArgs) {
		if !declaredMockConnection(session, plan, o.Connection) {
			return exitcode.Fail(exitcode.Configuration, errors.New("undeclared mock connection"))
		}
		cliVersion := strings.TrimSpace(tool.Str(plan["cliVersion"]))
		if cliVersion == "" {
			cliVersion = "unknown (mock; recorded native CLI version unavailable)"
		}
		_, err := fmt.Fprintln(os.Stdout, cliVersion)
		return err
	}

	artifact, err := lifecycle.LatestArtifact(o.SessionDir, session, o.Recording, false)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
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

	if _, _, _, err := loadMockSource(artifact.Path, session.AgentType); err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	external := o
	external.Recording = artifact.Path
	external.Agent = session.AgentType
	external.Report = connectionPath
	external.phase = o.Connection
	external.SessionDir = ""
	external.Connection = ""
	return mockExternalSelected(ctx, external)
}

func versionProbe(args []string) bool {
	return len(args) == 1 && (args[0] == "--version" || args[0] == "-v")
}

func declaredMockConnection(session *lifecycle.Session, plan tool.Object, id string) bool {
	return slices.Contains(lifecycle.ConnectionIDs(session.Source, plan), id)
}

func mockConnectionPaths(run *lifecycle.ActionRun, id string) (string, string, error) {
	for _, connection := range run.Execution.Connections {
		if connection.ID == id {
			return connection.Receipt, connection.ProcessReceipt, nil
		}
	}
	return "", "", fmt.Errorf("undeclared mock connection %s", id)
}
