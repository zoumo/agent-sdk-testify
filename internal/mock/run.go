package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// mockManagedAlias is the only no-session compatibility path. It creates one
// transient prepared session, executes one main mock connection, then finishes
// that same session. The caller's report receives the session, execution and
// finish evidence before the transient session directory is removed.
func mockManagedAlias(ctx context.Context, o Options) (err error) {
	if o.Connection != "" && o.Connection != "main" {
		return bad(errors.New("managed mock alias supports only the main connection"))
	}
	if o.Report == "" {
		return bad(errors.New("managed mock requires --report FILE"))
	}
	reportPath, sourceRoot, err := managedReportPath(o.Recording, o.Report)
	if err != nil {
		return bad(err)
	}

	report := object{
		"schemaVersion":   1,
		"managed":         true,
		"status":          "failed",
		"fullConsumption": false,
		"clientEOF":       false,
		"nativeLaunched":  false,
	}
	defer func() {
		report["exitCode"] = exitcode.Code(err)
		report["status"] = "failed"
		if err == nil {
			report["status"] = "passed"
		} else {
			report["error"] = err.Error()
		}
		err = errors.Join(err, writeManagedReport(reportPath, report))
	}()

	if o.Case != "" && o.Agent != "" {
		if _, _, err := builtin.Select(o.Agent, o.Case); err != nil {
			return bad(err)
		}
	}
	base, err := managedSessionBase(sourceRoot)
	if err != nil {
		return bad(err)
	}
	defer os.RemoveAll(base)
	sessionDir := filepath.Join(base, "session")
	session, prepareErr := lifecycle.Prepare(lifecycle.PrepareOptions{
		SessionDir: sessionDir,
		Agent:      o.Agent,
		Case:       o.Case,
		Recording:  o.Recording,
	})
	if session != nil {
		report["sessionId"] = session.SessionID
		report["session"] = session
	}
	if prepareErr != nil {
		return bad(prepareErr)
	}

	managed := o
	managed.SessionDir = sessionDir
	managed.Case = ""
	managed.Recording = ""
	managed.Report = ""
	managed.Connection = "main"
	mockErr := mockSession(ctx, managed)

	execution, evidence, inspectErr := managedExecutionEvidence(sessionDir)
	if execution != nil {
		report["executionId"] = execution.ExecutionID
		report["execution"] = execution
	}
	for key, value := range evidence {
		report[key] = value
	}
	finish, finishErr := lifecycle.Finish(sessionDir)
	report["finish"] = finish
	if finish != nil {
		report["cleanupComplete"] = finish.CleanupComplete
	}
	return errors.Join(mockErr, inspectErr, finishErr)
}

func managedReportPath(recording, report string) (string, string, error) {
	reportPath, err := filepath.Abs(report)
	if err != nil {
		return "", "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(reportPath))
	if err != nil {
		return "", "", err
	}
	reportPath = filepath.Join(parent, filepath.Base(reportPath))

	sourceRoot := ""
	if recording != "" {
		if !filepath.IsAbs(recording) {
			return "", "", errors.New("--recording must be an absolute path")
		}
		sourceRoot, err = filepath.EvalSymlinks(recording)
		if err != nil {
			return "", "", err
		}
		if rel, relErr := filepath.Rel(sourceRoot, reportPath); relErr != nil || filepath.IsLocal(rel) {
			return "", "", errors.New("report must be outside the recording directory")
		}
	}
	f, err := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	if err := f.Close(); err != nil {
		return "", "", err
	}
	return reportPath, sourceRoot, nil
}

func managedSessionBase(sourceRoot string) (string, error) {
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", err
	}
	if sourceRoot != "" {
		if rel, relErr := filepath.Rel(sourceRoot, base); relErr != nil || filepath.IsLocal(rel) {
			return "", errors.New("temporary directory must be outside recording")
		}
	}
	base, err = os.MkdirTemp(base, "agent-sdk-testify-alias-")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(base)
}

type managedIndex struct {
	Executions []struct {
		ID      string `json:"id"`
		Command string `json:"command"`
	} `json:"executions"`
}

func managedExecutionEvidence(sessionDir string) (*lifecycle.Execution, object, error) {
	var index managedIndex
	if err := tool.ReadJSON(lifecycle.IndexPath(sessionDir), &index); err != nil {
		return nil, nil, err
	}
	var id string
	for _, execution := range index.Executions {
		if execution.Command == "mock" {
			id = execution.ID
		}
	}
	if id == "" {
		return nil, nil, errors.New("managed mock did not create an execution")
	}
	dir := lifecycle.ExecutionDir(sessionDir, id)
	var execution lifecycle.Execution
	if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
		return nil, nil, err
	}
	evidence := object{}
	for _, connection := range execution.Connections {
		if connection.ID != "main" {
			continue
		}
		var receipt object
		if err := tool.ReadJSON(connection.Receipt, &receipt); err != nil {
			return &execution, evidence, err
		}
		for _, key := range []string{
			"status", "provider", "planSHA256", "recordingSHA256",
			"fullConsumption", "clientEOF", "nativeLaunched",
		} {
			if value, ok := receipt[key]; ok {
				evidence[key] = value
			}
		}
		break
	}
	evidence["provenance"] = execution.Artifact.Provenance
	return &execution, evidence, nil
}

func writeManagedReport(path string, value object) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("write managed mock report: %w", err)
	}
	return nil
}

func atomicMockJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mock-json-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
