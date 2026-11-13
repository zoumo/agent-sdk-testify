// Package record owns native protocol capture in a prepared execution.
package record

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

// Prepared is the validated target and stream contract for one record policy.
type Prepared struct {
	FixtureDir string
	Connection string
	Args       []string
	NativeArgs []string
	Command    string
	Remote     *capture.ConnectOptions
	Target     string
	Timeout    time.Duration
	ResultFD   int
}

func Run(ctx context.Context, prepared Prepared, streams capture.IO) (*lifecycle.RecordResult, error) {
	return lifecycle.RunRecord(ctx, lifecycle.RecordRequest{
		FixtureDir: prepared.FixtureDir,
		Connection: prepared.Connection,
		Args:       prepared.Args,
		Timeout:    prepared.Timeout,
		Target:     prepared.Target,
		ResultFD:   prepared.ResultFD,
	}, recordPolicy{
		command: prepared.Command,
		args:    prepared.NativeArgs,
		remote:  prepared.Remote,
		timeout: prepared.Timeout,
		streams: streams,
	})
}

type recordPolicy struct {
	command string
	args    []string
	remote  *capture.ConnectOptions
	timeout time.Duration
	streams capture.IO
}

func (p recordPolicy) Prepare(ctx context.Context, run lifecycle.RecordContext, trackProbe lifecycle.RecordProbeTracker) (tool.Object, error) {
	plan := run.Plan()
	if !run.IsOfficialCapture() {
		return plan, nil
	}
	return prepareOfficialCapture(ctx, run, p.command, p.args, p.remote, trackProbe)
}

func (p recordPolicy) Capture(ctx context.Context, run lifecycle.RecordContext) error {
	return captureNative(ctx, run, p.command, p.args, p.remote, p.timeout, p.streams)
}

func (p recordPolicy) Package(_ context.Context, run lifecycle.RecordContext) (*corpus.Material, error) {
	return packageNative(run)
}

func captureNative(ctx context.Context, run lifecycle.RecordContext, command string, args []string, remote *capture.ConnectOptions, timeout time.Duration, streams capture.IO) error {
	execution := run.Execution()
	plan := run.Plan()
	if run.IsOfficialCapture() && remote == nil {
		args = tool.Strings(plan["args"])
	}

	var commandArgs []string
	if remote == nil {
		commandArgs = append([]string{command}, args...)
	} else {
		remote.MaxFrameBytes = execution.Limits.MaxFrameBytes
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(recordReceipt(run))), 0o700); err != nil {
		return err
	}
	eofAction := tool.Str(plan["stdinEOFAction"])
	if eofAction == "" {
		eofAction = "close"
	}
	captureErr := capture.Run(ctx, capture.Options{
		Remote:         remote,
		ProcessFile:    run.ProcessReceipt(),
		Output:         filepath.Dir(recordReceipt(run)),
		CWD:            execution.Workspace,
		Env:            environment(execution.RuntimeEnv),
		ClearEnv:       true,
		Command:        commandArgs,
		NativeVersion:  tool.Str(plan["cliVersion"]),
		ProxyVersion:   version.String(),
		StdinEOFAction: eofAction,
		Timeout:        timeout,
		ProtocolBytes:  execution.Limits.ProtocolBytes,
		StderrBytes:    execution.Limits.StderrBytes,
		MaxFrames:      execution.Limits.MaxFrames,
		MaxFrameBytes:  execution.Limits.MaxFrameBytes,
		CleanupTimeout: 2 * time.Second,
	}, streams)
	return captureErr
}

// Only the final declared connection closes the complete material. Driver business
// reports are deliberately absent: they may be produced after this publication.
func packageNative(run lifecycle.RecordContext) (*corpus.Material, error) {
	execution := run.Execution()
	fixture := run.Fixture()
	planPath := execution.Source.Path

	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		return nil, err
	}
	if _, err := lifecycle.LoadPlan(&fixture); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(run.ArtifactRoot(), "source-plan.json"), planBytes, 0o600); err != nil {
		return nil, err
	}
	derived := run.Plan()
	if run.IsOfficialCapture() {
		if err := tool.ReadJSON(filepath.Join(run.OverlayRoot(), "material-plan.json"), &derived); err != nil {
			return nil, err
		}
		if err := tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "official-origin.json"), execution.Artifact.OfficialOrigin); err != nil {
			return nil, err
		}
		for _, connection := range execution.Connections {
			if err := tool.CopyFile(filepath.Join(run.ExecutionDir(), "receipts", connection.ID+".origin.json"), filepath.Join(run.ArtifactRoot(), "origin", connection.ID+".json")); err != nil {
				return nil, err
			}
		}
	}
	derived["sourcePlanSHA256"] = tool.DigestBytes(planBytes)
	if fixture.HistorySeed != "" {
		if err := lifecycle.VerifyHistorySeed(&fixture); err != nil {
			return nil, err
		}
	}
	seed := ""
	if fixture.HistorySeed != "" {
		seed = filepath.Join(run.ArtifactRoot(), "history-source")
	}
	startupDerived, err := history.StartupDerived(run.ExecutionDir())
	if err != nil {
		return nil, err
	}
	bindings, err := history.CaptureCanonical(seed, run.OverlayRoot(), run.ArtifactRoot(), derived, startupDerived)
	if err != nil {
		return nil, err
	}
	if seed != "" {
		prefix := "executions/" + execution.ExecutionID + "/capture"
		if err := history.RebindMaterialization(filepath.Dir(filepath.Dir(run.ExecutionDir())), run.ExecutionDir(), prefix+"/history-source", prefix+"/history/seed"); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(seed); err != nil {
			return nil, err
		}
	}
	derived["historyBindings"] = bindings
	if old, ok := derived["historySeed"]; ok {
		seed := tool.Obj(old)
		clean := tool.Object{}
		for _, key := range []string{"provider", "sourceProject", "sessionId", "agentIds", "materialPlan"} {
			if v, ok := seed[key]; ok {
				clean[key] = v
			}
		}
		derived["historySeed"] = clean
	}
	encoded, err := corpus.MaterialPlanBytes(planBytes, derived)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(run.ArtifactRoot(), "plan.json"), encoded, 0o600); err != nil {
		return nil, err
	}
	provenance := execution.Provenance
	if provenance == "official" {
		provenance = "capture"
	}
	manifest, err := writeCaptureManifest(run.ArtifactRoot(), execution.RuntimeEnv, provenance, nil)
	if err != nil {
		return nil, err
	}
	if manifest["captureStatus"] != "complete" {
		return nil, errors.New("record capture is incomplete")
	}
	var transport tool.Object
	if err := tool.ReadJSON(filepath.Join(run.ArtifactRoot(), "raw", "transport.json"), &transport); err != nil {
		return nil, err
	}
	report := tool.Object{"schemaVersion": 1, "status": "captured", "branchStatus": "not-applicable", "captureStatus": "complete", "consumer": execution.Provenance == "consumer", "provenance": execution.Provenance}
	for _, key := range []string{"nativeLaunched", "clientEOF", "cleanupComplete", "target", "remoteConnected", "remoteClosed"} {
		if value, ok := transport[key]; ok {
			report[key] = value
		}
	}
	if err := tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "report.json"), report); err != nil {
		return nil, err
	}
	if err := corpus.SealCoreInventory(run.ArtifactRoot()); err != nil {
		return nil, err
	}
	artifact, err := captureArtifact(run, execution)
	if err != nil {
		return nil, err
	}
	return artifact, nil
}

func recordReceipt(run lifecycle.RecordContext) string {
	if receipt := run.ConnectionReceipt(); receipt != "" {
		return receipt
	}
	return filepath.Join(run.ArtifactRoot(), "raw", "transport.json")
}

func environment(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func captureArtifact(run lifecycle.RecordContext, execution lifecycle.Execution) (*corpus.Material, error) {
	planHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "plan.json"))
	if err != nil {
		return nil, err
	}
	manifestHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "manifest.json"))
	if err != nil {
		return nil, err
	}
	rawHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "raw", "raw.jsonl"))
	if err != nil {
		return nil, err
	}
	artifact := execution.Artifact
	artifact.Kind = "capture"
	artifact.Action = "record"
	artifact.Provenance = execution.Provenance
	artifact.Path = run.ArtifactRoot()
	artifact.FormatVersion = corpus.MaterialFormatVersion
	artifact.SourcePlanSHA256, err = tool.FileDigest(filepath.Join(run.ArtifactRoot(), "source-plan.json"))
	if err != nil {
		return nil, err
	}
	if artifact.OfficialOrigin != nil {
		artifact.OriginSHA256, err = tool.FileDigest(filepath.Join(run.ArtifactRoot(), "official-origin.json"))
		if err != nil {
			return nil, err
		}
	}
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
