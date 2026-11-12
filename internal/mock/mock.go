package mock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/replay"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

// Prepared is the validated current-material contract for one replay policy.
type Prepared struct {
	FixtureDir  string
	Connection  string
	ExecutionID string
	Agent       string
	Artifact    corpus.Material
	Timeout     time.Duration
	ResultFD    int
	ProgressFD  int
	NativeArgs  []string
}

type Result = lifecycle.MockResult

// Run replays the current recording in one prepared-fixture execution.
func Run(ctx context.Context, prepared Prepared, input, output *os.File) (*Result, error) {
	return lifecycle.RunMock(ctx, lifecycle.MockRequest{
		FixtureDir:  prepared.FixtureDir,
		Connection:  prepared.Connection,
		ExecutionID: prepared.ExecutionID,
		NativeArgs:  prepared.NativeArgs,
		Timeout:     prepared.Timeout,
		ProgressFD:  prepared.ProgressFD,
		ResultFD:    prepared.ResultFD,
		Artifact:    prepared.Artifact,
	}, mockReplayPolicy{prepared: prepared, input: input, output: output})
}

func replayConnection(ctx context.Context, prepared Prepared, input, output *os.File, sourceRoot, recordingReference, materialID, executionID, phase, reportPath string) (err error) {
	var notification *os.File
	if prepared.ProgressFD >= 3 {
		fd, e := syscall.Dup(prepared.ProgressFD)
		if e != nil {
			return e
		}
		syscall.CloseOnExec(fd)
		notification = os.NewFile(uintptr(fd), "consumption-notification")
		defer notification.Close()
	}
	receiptFile, e := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return e
	}
	receipt := tool.Object{
		"schemaVersion": 2, "version": 1, "materialId": materialID, "executionId": executionID, "connectionId": phase, "status": "failed", "peer": "Go native mock", "nativeLaunched": false,
		"fullConsumption": false, "clientEOF": false, "cleanupComplete": true, "recording": recordingReference, "toolVersion": version.String(),
	}
	defer func() {
		receipt["exitCode"] = 0
		if err != nil {
			receipt["exitCode"] = 1
			receipt["error"] = err.Error()
		}
		closeErr := receiptFile.Close()
		err = errors.Join(err, closeErr, atomicMockJSON(reportPath, receipt))
	}()
	manifest, document, e := loadMockPhase(sourceRoot, prepared.Agent, phase)
	if e != nil {
		return e
	}
	receipt["provider"] = prepared.Agent
	receipt["planSHA256"] = manifest["planSHA256"]
	receipt["provenance"] = manifest["provenance"]
	receipt["recordingSHA256"] = document.SHA256
	receipt["frames"] = len(document.Frames)
	ctx, cancel := context.WithTimeout(ctx, replayTimeout(prepared.Agent, prepared.Timeout, document.Timeout))
	defer cancel()
	consumed, e := mockProtocolNotifying(ctx, document, notification, input, output)
	receipt["fullConsumption"] = consumed
	if e != nil {
		return e
	}
	receipt["status"], receipt["fullConsumption"], receipt["clientEOF"] = "passed", true, true
	return nil
}

func mockProtocolNotifying(ctx context.Context, document *recording.Document, notification, input, output *os.File) (bool, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return false, err
	}
	// Transfer a raw duplicate to Run; retaining an os.File wrapper for the same
	// descriptor would let its finalizer close a later, unrelated descriptor.
	fd, dupErr := syscall.Dup(int(w.Fd()))
	closeErr := w.Close()
	if dupErr != nil || closeErr != nil {
		if dupErr == nil {
			_ = syscall.Close(fd)
		}
		return false, errors.Join(dupErr, closeErr, r.Close())
	}
	syscall.CloseOnExec(fd)
	type progressResult struct {
		consumed bool
		err      error
	}
	done := make(chan progressResult, 1)
	go func() {
		var marker [1]byte
		n, e := r.Read(marker[:])
		consumed := n == 1 && marker[0] == '1'
		if errors.Is(e, io.EOF) {
			e = nil
		}
		if consumed && notification != nil {
			_, writeErr := notification.Write(marker[:])
			e = errors.Join(e, writeErr)
		}
		done <- progressResult{consumed, e}
	}()
	err = replay.Run(ctx, document, recording.Limits{}, "", input, output, fd)
	progress := <-done
	return progress.consumed, errors.Join(err, progress.err, r.Close())
}

func loadMockPhase(sourceRoot, provider, phase string) (tool.Object, *recording.Document, error) {
	// Unlike legacy shared-material loaders, this entry requires the source
	// manifest to bind both the plan and the raw stream, even for synthetic input.
	var manifest, plan tool.Object
	if readErr := tool.ReadJSON(filepath.Join(sourceRoot, "manifest.json"), &manifest); readErr != nil {
		return nil, nil, readErr
	}
	rawName := "raw.jsonl"
	if manifest["materialFormatVersion"] == float64(3) {
		rawName = "raw/raw.jsonl"
	}
	if tool.Str(manifest["planSHA256"]) == "" || tool.Str(tool.Obj(manifest["files"])[rawName]) == "" {
		return nil, nil, errors.New("source manifest requires planSHA256 and files.raw.jsonl")
	}
	if err := corpus.VerifySourcePhase(sourceRoot, true); err != nil {
		return nil, nil, err
	}
	if err := tool.ReadJSON(filepath.Join(sourceRoot, "plan.json"), &plan); err != nil {
		return nil, nil, err
	}
	plannedProvider, err := planAgentIdentity(plan)
	if err != nil {
		return nil, nil, err
	}
	if provider == "" {
		provider = plannedProvider
	} else if plannedProvider != "" && provider != plannedProvider {
		return nil, nil, errors.New("agent type differs from selected recording")
	}
	rawRoot := filepath.Join(sourceRoot, "raw")
	expected := tool.Str(tool.Obj(manifest["files"])[rawName])
	if phase == "probe" && plan["driverProfile"] == "remote-close" {
		rawRoot = filepath.Join(sourceRoot, "remote-probe", "raw")
		expected = tool.Str(tool.Obj(manifest["remotePhaseFiles"])["remote-probe/raw/raw.jsonl"])
		if manifest["materialFormatVersion"] == float64(3) {
			expected = tool.Str(tool.Obj(manifest["files"])["remote-probe/raw/raw.jsonl"])
		}
		if expected == "" {
			return nil, nil, errors.New("source manifest must bind selected probe stream")
		}
		if err := corpus.VerifySourcePhase(filepath.Join(sourceRoot, "remote-probe"), true); err != nil {
			return nil, nil, err
		}
	} else if phase != "main" {
		return nil, nil, errors.New("undeclared recording phase")
	}
	source, err := os.Open(filepath.Join(rawRoot, "raw.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	document, readErr := recording.Read(source, provider, recording.Limits{})
	if err := errors.Join(readErr, source.Close()); err != nil {
		return nil, nil, err
	}
	if document.SHA256 != expected {
		return nil, nil, errors.New("source hash mismatch: raw.jsonl")
	}
	// Legacy manifests have no format descriptor. If one is supplied, enforce
	// its version and transport rather than treating it as decorative metadata.
	for _, name := range []string{filepath.Join(sourceRoot, "manifest.json"), filepath.Join(rawRoot, "manifest.json")} {
		data, readErr := os.ReadFile(name)
		if os.IsNotExist(readErr) {
			continue
		}
		var metadata tool.Object
		if readErr != nil {
			return nil, nil, readErr
		}
		if err := json.Unmarshal(data, &metadata); err != nil {
			return nil, nil, err
		}
		if _, present := metadata["recordingFormat"]; present && metadata["materialFormatVersion"] != float64(3) {
			content := document.Original
			// The top manifest's files.raw.jsonl binds main. Probe bytes are bound by
			// remotePhaseFiles and their own raw manifest; validate each against its file.
			if phase == "probe" && name == filepath.Join(sourceRoot, "manifest.json") {
				content, err = os.ReadFile(filepath.Join(sourceRoot, "raw", "raw.jsonl"))
				if err != nil {
					return nil, nil, err
				}
			}
			if _, err := recording.ValidateManifest(data, "raw.jsonl", content); err != nil {
				return nil, nil, err
			}
		}
	}
	if provider != "codex" && provider != "claude" {
		return nil, nil, errors.New("materials must identify codex or claude")
	}
	return manifest, document, nil
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

func replayTimeout(agent string, explicit, scenario time.Duration) time.Duration {
	if explicit != 0 {
		return explicit
	}
	if scenario != 0 {
		return scenario
	}
	if agent == "claude" {
		return 10 * time.Second
	}
	return 30 * time.Second
}
