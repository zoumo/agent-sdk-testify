// Package record owns native protocol capture in a prepared execution.
package record

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type Options struct {
	SessionDir string
	URL        string
	TokenFile  string
	Command    string
	NativeArgs []string
	Timeout    time.Duration
	ResultFD   int
}

func Run(ctx context.Context, o Options, streams capture.IO) error {
	return run(ctx, o, streams, false)
}

// RunLegacy retains the old official orchestration until its replacement lands.
func RunLegacy(ctx context.Context, o Options, streams capture.IO) error {
	return run(ctx, o, streams, true)
}

func run(ctx context.Context, o Options, streams capture.IO, legacy bool) (runErr error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	result, err := lifecycle.ResultStream(o.ResultFD)
	if err != nil {
		return err
	}
	if result != nil {
		defer result.Close()
	}
	var executable string
	var remote *capture.ConnectOptions
	if o.URL != "" || o.TokenFile != "" {
		if legacy || o.URL == "" || o.TokenFile == "" || o.Command != "" || len(o.NativeArgs) != 0 {
			return errors.New("remote record requires --url and --token-file, excluding local command and arguments")
		}
		remote = &capture.ConnectOptions{URL: o.URL, TokenFile: o.TokenFile}
		if err := remote.Validate(); err != nil {
			return err
		}
	} else {
		executable, err = validateCommand(o.Command)
		if err != nil {
			return err
		}
	}
	o.SessionDir, err = filepath.EvalSymlinks(o.SessionDir)
	if err != nil {
		return err
	}
	if remote != nil {
		tokenPath, pathErr := filepath.EvalSymlinks(remote.TokenFile)
		if pathErr != nil {
			return errors.New("private token file unavailable")
		}
		if rel, relErr := filepath.Rel(o.SessionDir, tokenPath); relErr == nil && filepath.IsLocal(rel) {
			return errors.New("token file must be outside the persistent session")
		}
	}
	session, err := lifecycle.OpenActionSession(o.SessionDir)
	if err != nil {
		return err
	}
	if remote == nil {
		if err := protocol.ValidateNativeArgs(session.AgentType, o.NativeArgs); err != nil {
			return err
		}
	}
	commandArgs := append([]string{"--session-dir", o.SessionDir, "--cmd", executable, "--"}, o.NativeArgs...)
	if remote != nil {
		// Connection location and authentication paths are invocation-only data.
		commandArgs = []string{"--session-dir", o.SessionDir, "--url", "<private>", "--token-file", "<private>"}
	}
	run, err := lifecycle.BeginRecord(o.SessionDir, commandArgs, o.Timeout)
	if err != nil {
		return err
	}
	status, artifactID := "failed", ""
	defer func() {
		if status != "passed" {
			scrubErr := lifecycle.ScrubExecutionAuthentication(run)
			finishErr := lifecycle.FinishAction(run, status, artifactID)
			runErr = errors.Join(runErr, scrubErr, finishErr)
			evidence := tool.Object{"schemaVersion": 1, "status": "failed", "error": runErr.Error(), "cleanupComplete": scrubErr == nil && finishErr == nil}
			runErr = errors.Join(runErr, tool.WriteJSON(filepath.Join(run.ArtifactRoot, "failure.json"), evidence))
		}
		runErr = errors.Join(runErr, lifecycle.WriteActionResult(ctx, run, result, runErr))
		runErr = errors.Join(runErr, run.Close())
	}()
	if err := lifecycle.NotifyActionStarted(ctx, run, result); err != nil {
		return err
	}
	if legacy && session.Source.Provenance != "consumer" {
		runErr = recordOfficial(ctx, run, executable, o.NativeArgs, o.Timeout, streams.Out)
	} else {
		runErr = captureNative(ctx, run, executable, o.NativeArgs, remote, o.Timeout, streams)
	}
	if runErr == nil {
		status = "passed"
		artifactID = run.Execution.Artifact.ID
	}
	return runErr
}

func validateCommand(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", errors.New("--cmd must be an absolute executable path")
	}
	value, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(value)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("--cmd must be a regular executable")
	}
	return value, nil
}

func recordOfficial(ctx context.Context, run *lifecycle.ActionRun, command string, args []string, timeout time.Duration, stdout io.Writer) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	driver, err := official.ResolveDriver(run.Session.AgentType)
	if err != nil {
		return err
	}
	summary, err := official.Run(ctx, official.Options{
		Mode:           "record",
		Case:           run.Session.Source.ID,
		Provider:       run.Session.AgentType,
		Driver:         driver,
		Output:         run.OverlayRoot,
		Workspace:      run.Execution.Workspace,
		OutputPrepared: true,
		Plan:           run.Plan,
		HistorySeed:    run.Session.HistorySeed,
		NativeCommand:  command,
		NativeArgs:     args,
		RuntimeEnv:     run.Execution.RuntimeEnv,
	})
	if summary != nil {
		if encodeErr := json.NewEncoder(stdout).Encode(summary); encodeErr != nil {
			err = errors.Join(err, fmt.Errorf("encode record summary: %w", encodeErr))
		}
	}
	if snapshotErr := snapshotOfficial(run.OverlayRoot, run.ArtifactRoot); snapshotErr != nil {
		return errors.Join(err, snapshotErr)
	}
	if err != nil {
		return err
	}
	artifact, err := captureArtifact(run, "official")
	if err != nil {
		return err
	}
	if snapshotComplete(run.ArtifactRoot) {
		return lifecycle.PublishCapture(filepath.Dir(filepath.Dir(run.Dir)), run, *artifact)
	}
	return errors.New("official record capture is incomplete")
}

func captureNative(ctx context.Context, run *lifecycle.ActionRun, command string, args []string, remote *capture.ConnectOptions, timeout time.Duration, streams capture.IO) error {
	planPath := run.Execution.Source.Path
	if run.Execution.Source.Kind == "recording" {
		planPath = filepath.Join(planPath, "plan.json")
	}
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var commandArgs []string
	if remote == nil {
		commandArgs = append([]string{command}, args...)
	} else {
		remote.MaxFrameBytes = run.Execution.Limits.MaxFrameBytes
	}
	captureErr := capture.Run(ctx, capture.Options{
		Remote:         remote,
		Output:         filepath.Join(run.ArtifactRoot, "raw"),
		CWD:            run.Execution.Workspace,
		Env:            environment(run.Execution.RuntimeEnv),
		ClearEnv:       true,
		Command:        commandArgs,
		NativeVersion:  tool.Str(run.Plan["cliVersion"]),
		ProxyVersion:   version.Version,
		StdinEOFAction: "close",
		Timeout:        timeout,
		ProtocolBytes:  run.Execution.Limits.ProtocolBytes,
		StderrBytes:    run.Execution.Limits.StderrBytes,
		MaxFrames:      run.Execution.Limits.MaxFrames,
		MaxFrameBytes:  run.Execution.Limits.MaxFrameBytes,
		CleanupTimeout: 2 * time.Second,
	}, streams)
	if err := os.WriteFile(filepath.Join(run.ArtifactRoot, "plan.json"), planBytes, 0o600); err != nil {
		return errors.Join(captureErr, err)
	}
	if _, err := lifecycle.LoadPlan(run.Session); err != nil {
		return errors.Join(captureErr, err)
	}
	if err := snapshotHistory(run.OverlayRoot, run.ArtifactRoot); err != nil {
		return errors.Join(captureErr, err)
	}
	manifest, manifestErr := writeCaptureManifest(run.ArtifactRoot, run.Execution.RuntimeEnv, run.Execution.Provenance, captureErr)
	if manifestErr != nil {
		return errors.Join(captureErr, manifestErr)
	}
	var transport tool.Object
	if err := tool.ReadJSON(filepath.Join(run.ArtifactRoot, "raw", "transport.json"), &transport); err != nil {
		return errors.Join(captureErr, err)
	}
	report := map[string]any{
		"schemaVersion":   1,
		"status":          "failed",
		"branchStatus":    "not-applicable",
		"captureStatus":   manifest["captureStatus"],
		"nativeLaunched":  transport["nativeLaunched"],
		"clientEOF":       transport["clientEOF"],
		"cleanupComplete": transport["cleanupComplete"],
		"consumer":        run.Execution.Provenance == "consumer",
		"provenance":      run.Execution.Provenance,
	}
	if remote != nil {
		for _, key := range []string{"target", "remoteConnected", "remoteClosed"} {
			report[key] = transport[key]
		}
	}
	if captureErr == nil && manifest["captureStatus"] == "complete" {
		report["status"] = "captured"
	}
	if captureErr != nil {
		report["error"] = captureErr.Error()
	}
	if err := tool.WriteJSON(filepath.Join(run.ArtifactRoot, "report.json"), report); err != nil {
		return errors.Join(captureErr, err)
	}
	if captureErr != nil {
		return captureErr
	}
	if manifest["captureStatus"] != "complete" {
		return errors.New("record capture is incomplete")
	}
	artifact, err := captureArtifact(run, run.Execution.Provenance)
	if err != nil {
		return err
	}
	return lifecycle.PublishCapture(filepath.Dir(filepath.Dir(run.Dir)), run, *artifact)
}

func environment(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func snapshotOfficial(source, target string) error {
	for _, name := range []string{
		"plan.json",
		"manifest.json",
		"report.json",
		"result.json",
		"summary.json",
		"remote-service.json",
		"request.json",
		"source-report.json",
		"stderr.log",
		"stdout.log",
	} {
		if err := copyEvidenceFile(source, target, name); err != nil {
			return err
		}
	}
	for _, name := range []string{"raw", "remote-probe/raw", "config/projects", "config/sessions", "history-seed"} {
		if err := copyEvidenceTree(source, target, name); err != nil {
			return err
		}
	}
	return nil
}

func snapshotHistory(source, target string) error {
	for _, name := range []string{"config/projects", "config/sessions", "history-seed"} {
		if err := copyEvidenceTree(source, target, name); err != nil {
			return err
		}
	}
	return nil
}

func copyEvidenceFile(source, target, name string) error {
	path := filepath.Join(source, filepath.FromSlash(name))
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("capture evidence is not regular: %s", name)
	}
	return tool.CopyFile(path, filepath.Join(target, filepath.FromSlash(name)))
}

func copyEvidenceTree(source, target, name string) error {
	path := filepath.Join(source, filepath.FromSlash(name))
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("capture history is not an unlinked directory: %s", name)
	}
	return tool.CopyTree(path, filepath.Join(target, filepath.FromSlash(name)))
}

func snapshotComplete(root string) bool {
	var manifest, transport map[string]any
	if tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest) != nil {
		return false
	}
	if tool.ReadJSON(filepath.Join(root, "raw", "transport.json"), &transport) != nil {
		return false
	}
	return manifest["captureStatus"] == "complete" && transport["captureStatus"] == "complete" && transport["cleanupComplete"] == true
}

func captureArtifact(run *lifecycle.ActionRun, provenance string) (*lifecycle.Artifact, error) {
	planHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot, "plan.json"))
	if err != nil {
		return nil, err
	}
	manifestHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot, "manifest.json"))
	if err != nil {
		return nil, err
	}
	rawHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot, "raw", "raw.jsonl"))
	if err != nil {
		return nil, err
	}
	artifact := run.Execution.Artifact
	artifact.Kind = "capture"
	artifact.Action = "record"
	artifact.Provenance = provenance
	artifact.Path = run.ArtifactRoot
	artifact.PlanSHA256 = planHash
	artifact.ManifestSHA256 = manifestHash
	artifact.RecordingSHA256 = rawHash
	artifact.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return &artifact, nil
}

func writeCaptureManifest(root string, env map[string]string, provenance string, captureErr error) (map[string]any, error) {
	files := map[string]any{}
	entries, err := os.ReadDir(filepath.Join(root, "raw"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		digest, err := tool.FileDigest(filepath.Join(root, "raw", entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = digest
	}
	saved := map[string]any{}
	for _, prefix := range []string{"config/projects", "config/sessions", "history-seed"} {
		base := filepath.Join(root, filepath.FromSlash(prefix))
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("history symlink is forbidden")
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			digest, err := tool.FileDigest(path)
			if err != nil {
				return err
			}
			saved[filepath.ToSlash(rel)] = digest
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	planHash, err := tool.FileDigest(filepath.Join(root, "plan.json"))
	if err != nil {
		return nil, err
	}
	var transport map[string]any
	if err := tool.ReadJSON(filepath.Join(root, "raw", "transport.json"), &transport); err != nil {
		return nil, err
	}
	runHash, err := tool.FileDigest(filepath.Join(root, "raw", "transport.json"))
	if err != nil {
		return nil, err
	}
	var rawManifest map[string]any
	if err := tool.ReadJSON(filepath.Join(root, "raw", "manifest.json"), &rawManifest); err != nil {
		return nil, err
	}
	scan, scanErr := corpus.ScanCaptureCredentialsWithEnvironment(root, env)
	if scanErr != nil {
		return nil, scanErr
	}
	status := "captured"
	if captureErr != nil || transport["captureStatus"] != "complete" {
		status = "failed"
	}
	manifest := map[string]any{
		"schemaVersion":   1,
		"provenance":      provenance,
		"status":          status,
		"planSHA256":      planHash,
		"runSHA256":       runHash,
		"files":           files,
		"savedFiles":      saved,
		"captureStatus":   transport["captureStatus"],
		"recordingFormat": recording.CurrentFormat(),
		"proxyVersion":    rawManifest["proxyVersion"],
		"native":          rawManifest["native"],
		"credentialScan":  scan,
	}
	if captureErr != nil {
		manifest["error"] = captureErr.Error()
	}
	if err := tool.WriteJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}
