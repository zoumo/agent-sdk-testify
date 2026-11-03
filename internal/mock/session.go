package mock

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	plan, err := lifecycle.LoadPlan(session)
	if err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	if len(o.NativeArgs) == 1 && (o.NativeArgs[0] == "--version" || o.NativeArgs[0] == "-v") {
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
	run, err := lifecycle.BeginMock(o.SessionDir, o.Connection, append([]string{"--session-dir", o.SessionDir, "--connection", o.Connection}, o.NativeArgs...), o.Timeout, artifact)
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

func declaredMockConnection(session *lifecycle.Session, plan tool.Object, id string) bool {
	if session.Source.Kind == "plan" {
		return id == "main"
	}
	if tool.Str(plan["driverProfile"]) == "remote-close" && session.Source.Provenance != "consumer" {
		return id == "main" || id == "probe"
	}
	return id == "main"
}

func mockConnectionPaths(run *lifecycle.ActionRun, id string) (string, string, error) {
	for _, connection := range run.Execution.Connections {
		if connection.ID == id {
			return connection.Receipt, connection.ProcessReceipt, nil
		}
	}
	return "", "", fmt.Errorf("undeclared mock connection %s", id)
}
