// Package record owns native protocol capture in a prepared execution.
package record

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type Options struct {
	SessionDir     string
	Connection     string
	URL            string
	TokenFile      string
	Command        string
	NativeArgs     []string
	Timeout        time.Duration
	ConnectTimeout time.Duration
	ResultFD       int
}

func Run(ctx context.Context, o Options, streams capture.IO) (runErr error) {
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
		if o.URL == "" || o.TokenFile == "" || o.Command != "" || len(o.NativeArgs) != 0 {
			return errors.New("remote record requires --url and --token-file, excluding local command and arguments")
		}
		remote = &capture.ConnectOptions{URL: o.URL, TokenFile: o.TokenFile, ConnectTimeout: o.ConnectTimeout}
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
	if o.Connection == "" {
		o.Connection = "main"
	}
	target := "local"
	if remote != nil {
		token, err := os.ReadFile(remote.TokenFile)
		if err != nil {
			return err
		}
		target = tool.DigestBytes(append(append([]byte(remote.URL), 0), token...))
	}
	run, err := lifecycle.BeginRecordConnection(o.SessionDir, o.Connection, commandArgs, o.Timeout, target)
	if err != nil {
		return err
	}
	status := "failed"
	defer func() {
		finalize, err := lifecycle.RecordFinalizationLock(run)
		if err != nil {
			runErr = errors.Join(runErr, err)
		} else {
			defer finalize.Close()
			ready, passed, err := lifecycle.CompleteRecordConnection(run, status)
			runErr = errors.Join(runErr, err)
			if ready && err == nil {
				if passed {
					runErr = errors.Join(runErr, publishNative(run))
				} else {
					runErr = errors.Join(runErr, errors.New("record execution has failed connections"))
				}
				if runErr != nil {
					runErr = errors.Join(runErr, lifecycle.ScrubExecutionAuthentication(run), lifecycle.FinishAction(run, "failed", ""))
				}
			}
		}
		if runErr != nil {
			runErr = errors.Join(runErr, tool.WriteJSON(filepath.Join(run.Dir, "receipts", run.Connection+".failure.json"), tool.Object{"status": "failed", "error": runErr.Error()}))
		}
		runErr = errors.Join(runErr, lifecycle.WriteActionResult(ctx, run, result, runErr))
		runErr = errors.Join(runErr, run.Close())
	}()
	if err := lifecycle.NotifyActionStarted(ctx, run, result); err != nil {
		return err
	}
	runErr = captureNative(ctx, run, executable, o.NativeArgs, remote, o.Timeout, streams)
	if runErr == nil {
		status = "passed"
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

func captureNative(ctx context.Context, run *lifecycle.ActionRun, command string, args []string, remote *capture.ConnectOptions, timeout time.Duration, streams capture.IO) error {
	if run.Execution.Artifact.OfficialOrigin != nil {
		if err := prepareOfficialCapture(ctx, run, command, args, remote); err != nil {
			return err
		}
		if remote == nil {
			args = tool.Strings(run.Plan["args"])
		}
	}

	var commandArgs []string
	if remote == nil {
		commandArgs = append([]string{command}, args...)
	} else {
		remote.MaxFrameBytes = run.Execution.Limits.MaxFrameBytes
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(recordReceipt(run))), 0o700); err != nil {
		return err
	}
	eofAction := tool.Str(run.Plan["stdinEOFAction"])
	if eofAction == "" {
		eofAction = "close"
	}
	captureErr := capture.Run(ctx, capture.Options{
		Remote:         remote,
		ProcessFile:    filepath.Join(run.Dir, "receipts", run.Connection+".native-process.json"),
		Output:         filepath.Dir(recordReceipt(run)),
		CWD:            run.Execution.Workspace,
		Env:            environment(run.Execution.RuntimeEnv),
		ClearEnv:       true,
		Command:        commandArgs,
		NativeVersion:  tool.Str(run.Plan["cliVersion"]),
		ProxyVersion:   version.Version,
		StdinEOFAction: eofAction,
		Timeout:        timeout,
		ProtocolBytes:  run.Execution.Limits.ProtocolBytes,
		StderrBytes:    run.Execution.Limits.StderrBytes,
		MaxFrames:      run.Execution.Limits.MaxFrames,
		MaxFrameBytes:  run.Execution.Limits.MaxFrameBytes,
		CleanupTimeout: 2 * time.Second,
	}, streams)
	return captureErr
}

// Only the final declared connection closes the complete material. Driver business
// reports are deliberately absent: they may be produced after this publication.
func publishNative(run *lifecycle.ActionRun) error {
	planPath := run.Execution.Source.Path
	if run.Execution.Source.Kind == "recording" {
		planPath = filepath.Join(planPath, "plan.json")
	}
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	if _, err := lifecycle.LoadPlan(run.Session); err != nil {
		return err
	}
	if run.Execution.Artifact.OfficialOrigin != nil {
		if err := os.WriteFile(filepath.Join(run.ArtifactRoot, "source-plan.json"), planBytes, 0o600); err != nil {
			return err
		}
		var derived tool.Object
		if err := tool.ReadJSON(filepath.Join(run.OverlayRoot, "record-plan.json"), &derived); err != nil {
			return err
		}
		derived["sourcePlanSHA256"] = tool.DigestBytes(planBytes)
		if err := tool.WriteJSON(filepath.Join(run.ArtifactRoot, "official-origin.json"), run.Execution.Artifact.OfficialOrigin); err != nil {
			return err
		}
		for _, connection := range run.Execution.Connections {
			if err := tool.CopyFile(filepath.Join(run.Dir, "receipts", connection.ID+".origin.json"), filepath.Join(run.ArtifactRoot, "origin", connection.ID+".json")); err != nil {
				return err
			}
		}
		encoded, err := json.MarshalIndent(derived, "", "  ")
		if err != nil {
			return err
		}
		planBytes = append([]byte(nil), encoded...)
		planBytes = append(planBytes, '\n')
	}
	if err := os.WriteFile(filepath.Join(run.ArtifactRoot, "plan.json"), planBytes, 0o600); err != nil {
		return err
	}
	if err := snapshotHistory(run.OverlayRoot, run.ArtifactRoot); err != nil {
		return err
	}
	provenance := run.Execution.Provenance
	if provenance == "official" {
		provenance = "capture"
	}
	manifest, err := writeCaptureManifest(run.ArtifactRoot, run.Execution.RuntimeEnv, provenance, nil)
	if err != nil {
		return err
	}
	if manifest["captureStatus"] != "complete" {
		return errors.New("record capture is incomplete")
	}
	var transport tool.Object
	if err := tool.ReadJSON(filepath.Join(run.ArtifactRoot, "raw", "transport.json"), &transport); err != nil {
		return err
	}
	report := tool.Object{"schemaVersion": 1, "status": "captured", "branchStatus": "not-applicable", "captureStatus": "complete", "consumer": run.Execution.Provenance == "consumer", "provenance": run.Execution.Provenance}
	for _, key := range []string{"nativeLaunched", "clientEOF", "cleanupComplete", "target", "remoteConnected", "remoteClosed"} {
		if value, ok := transport[key]; ok {
			report[key] = value
		}
	}
	if err := tool.WriteJSON(filepath.Join(run.ArtifactRoot, "report.json"), report); err != nil {
		return err
	}
	artifact, err := captureArtifact(run, run.Execution.Provenance)
	if err != nil {
		return err
	}
	return lifecycle.PublishCapture(filepath.Dir(filepath.Dir(run.Dir)), run, *artifact)
}

func recordReceipt(run *lifecycle.ActionRun) string {
	for _, c := range run.Execution.Connections {
		if c.ID == run.Connection {
			return c.Receipt
		}
	}
	return filepath.Join(run.ArtifactRoot, "raw", "transport.json")
}

func environment(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func snapshotHistory(source, target string) error {
	for _, name := range []string{"config/projects", "config/sessions", "history-seed"} {
		if err := copyEvidenceTree(source, target, name); err != nil {
			return err
		}
	}
	return nil
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
	remoteFiles := tool.Object{}
	probeRoot := filepath.Join(root, "remote-probe", "raw")
	if entries, err := os.ReadDir(probeRoot); err == nil {
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				return nil, errors.New("invalid probe capture file")
			}
			name := filepath.ToSlash(filepath.Join("remote-probe", "raw", entry.Name()))
			hash, err := tool.FileDigest(filepath.Join(root, name))
			if err != nil {
				return nil, err
			}
			remoteFiles[name] = hash
		}
	} else if !os.IsNotExist(err) {
		return nil, err
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
		"schemaVersion":    1,
		"provenance":       provenance,
		"status":           status,
		"planSHA256":       planHash,
		"runSHA256":        runHash,
		"files":            files,
		"savedFiles":       saved,
		"remotePhaseFiles": remoteFiles,
		"captureStatus":    transport["captureStatus"],
		"recordingFormat":  recording.CurrentFormat(),
		"proxyVersion":     rawManifest["proxyVersion"],
		"native":           rawManifest["native"],
		"credentialScan":   scan,
	}
	if digest, err := tool.FileDigest(filepath.Join(root, "source-plan.json")); err == nil {
		manifest["sourcePlanSHA256"] = digest
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if captureErr != nil {
		manifest["error"] = captureErr.Error()
	}
	if err := tool.WriteJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}
