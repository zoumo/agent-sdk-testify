package official

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type driverRunOptions struct {
	Activity   *lifecycle.OfficialRun
	Driver     string
	Env        map[string]string
	Workspace  string
	Root       string
	PrivateDir string
	Plan       tool.Object
	Peers      map[string]tool.Command
	ReportFile string
	// Inspect may add policy-owned evidence after the process and reports are
	// available. The runner hashes its writes together with the driver tree.
	Inspect func(outcome, report tool.Object, priorErr error) error
}

type driverRunResult struct {
	Process            tool.Object
	Outcome            tool.Object
	Report             tool.Object
	Files              map[string]string
	FileHashesComplete bool
	ProcessGroupFailed bool
}

func runDriver(ctx context.Context, o driverRunOptions) (*driverRunResult, error) {
	if err := os.MkdirAll(o.Root, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.PrivateDir, 0o700); err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	peer := DriverPeer{}
	results := tool.Object{}
	for id, command := range o.Peers {
		results[id] = filepath.Join(o.Root, "peer-"+id+".results.jsonl")
		request := filepath.Join(o.PrivateDir, "peer-"+id+".json")
		if err := tool.WriteJSON(request, command); err != nil {
			return nil, err
		}
		launcher, err := WritePeerLauncher(o.PrivateDir, id, tool.Command{Command: self, Args: []string{
			"--internal-peer",
			"--request", request,
			"--lifecycle", filepath.Join(o.Root, "peer-"+id+".process.json"),
			"--results", tool.Str(results[id]),
		}})
		if err != nil {
			return nil, err
		}
		entry := tool.Command{Command: launcher}
		switch id {
		case "main":
			peer.Command = entry
		case "probe":
			peer.Probe = &entry
		default:
			return nil, fmt.Errorf("unsupported driver connection %s", id)
		}
	}
	o.Plan["_agentResults"] = results
	materials := filepath.Join(o.Root, "materials")
	if err := os.MkdirAll(materials, 0o700); err != nil {
		return nil, err
	}
	if err := tool.WriteJSON(filepath.Join(materials, "case.json"), o.Plan); err != nil {
		return nil, err
	}
	requestPath := filepath.Join(o.Root, "request.json")
	if err := tool.WriteJSON(requestPath, Request{Case: tool.Str(o.Plan["caseId"]), Materials: materials, Workspace: o.Workspace, Peer: peer, Output: o.Root}); err != nil {
		return nil, err
	}
	stdout, err := os.OpenFile(filepath.Join(o.Root, "stdout.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	stderr, err := os.OpenFile(filepath.Join(o.Root, "stderr.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, errors.Join(err, stdout.Close())
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), o.Driver, "--request", requestPath)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = environment(o.Env), o.Workspace, stdout, stderr
	runErr := o.Activity.WaitDriver(ctx, cmd)
	_ = stdout.Close()
	_ = stderr.Close()
	result := &driverRunResult{
		Process: tool.Object{"started": cmd.Process != nil, "reaped": cmd.ProcessState != nil, "cleanupComplete": cmd.ProcessState != nil},
	}
	if cmd.ProcessState != nil {
		result.Process["exitCode"] = cmd.ProcessState.ExitCode()
	}
	if cmd.Process != nil {
		result.Process["pid"] = cmd.Process.Pid
		if err := tool.WaitProcessGroup(cmd.Process.Pid); err != nil {
			result.Process["cleanupComplete"] = false
			result.ProcessGroupFailed = true
			runErr = errors.Join(runErr, err)
		}
	}
	outcomeErr := tool.ReadJSON(filepath.Join(o.Root, "result.json"), &result.Outcome)
	reportErr := tool.ReadJSON(filepath.Join(o.Root, o.ReportFile), &result.Report)
	runErr = errors.Join(runErr, outcomeErr, reportErr)
	if o.Inspect != nil {
		runErr = errors.Join(runErr, o.Inspect(result.Outcome, result.Report, runErr))
	}
	var hashErr error
	result.Files, hashErr = history.HashTree(o.Root)
	result.FileHashesComplete = hashErr == nil
	return result, errors.Join(runErr, hashErr)
}
