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
	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
	"github.com/zoumo/agent-sdk-testify/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/protocol/replay"
)

type Options struct {
	SessionDir string
	Connection string
	Timeout    time.Duration
	ProgressFD int
	NativeArgs []string
}

func bad(err error) error { return exitcode.Fail(exitcode.Configuration, err) }

// Run replays the current recording in one prepared-session execution.
func Run(ctx context.Context, o Options) error {
	if o.SessionDir == "" {
		return bad(errors.New("mock requires --session-dir"))
	}
	return mockSession(ctx, o)
}

func replayConnection(ctx context.Context, o Options, sourceRoot, agent, phase, reportPath string) (err error) {
	var notification *os.File
	if o.ProgressFD >= 3 {
		fd, e := syscall.Dup(o.ProgressFD)
		if e != nil {
			return bad(e)
		}
		syscall.CloseOnExec(fd)
		notification = os.NewFile(uintptr(fd), "consumption-notification")
		defer notification.Close()
	}
	receiptFile, e := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return bad(e)
	}
	receipt := tool.Object{
		"schemaVersion": 1, "version": 1, "status": "failed", "peer": "Go native mock", "nativeLaunched": false,
		"fullConsumption": false, "clientEOF": false, "cleanupComplete": true, "recording": sourceRoot, "toolVersion": version.Version,
	}
	defer func() {
		receipt["exitCode"] = exitcode.Code(err)
		if err != nil {
			receipt["error"] = err.Error()
		}
		closeErr := receiptFile.Close()
		err = errors.Join(err, closeErr, atomicMockJSON(reportPath, receipt))
	}()
	manifest, document, e := loadMockPhase(sourceRoot, agent, phase)
	if e != nil {
		return bad(e)
	}
	receipt["provider"] = agent
	receipt["planSHA256"] = manifest["planSHA256"]
	receipt["provenance"] = manifest["provenance"]
	receipt["recordingSHA256"] = document.SHA256
	receipt["frames"] = len(document.Frames)
	timeout := o.Timeout
	if timeout == 0 {
		timeout = document.Timeout
	}
	if timeout == 0 {
		timeout = 30 * time.Second
		if agent == "claude" {
			timeout = 10 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	consumed, e := mockProtocolNotifying(ctx, document, notification)
	receipt["fullConsumption"] = consumed
	if e != nil {
		code := exitcode.Failure
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			code = exitcode.Canceled
		} else if errors.Is(e, replay.ErrDisconnected) {
			code = exitcode.Disconnect
		}
		return exitcode.Fail(code, e)
	}
	receipt["status"], receipt["fullConsumption"], receipt["clientEOF"] = "passed", true, true
	return nil
}

func mockProtocolNotifying(ctx context.Context, document *recording.Document, notification *os.File) (bool, error) {
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
	err = replay.Run(ctx, document, recording.Limits{}, "", os.Stdin, os.Stdout, fd)
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
	if tool.Str(manifest["planSHA256"]) == "" || tool.Str(tool.Obj(manifest["files"])["raw.jsonl"]) == "" {
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
	expected := tool.Str(tool.Obj(manifest["files"])["raw.jsonl"])
	if phase == "probe" && plan["driverProfile"] == "remote-close" {
		rawRoot = filepath.Join(sourceRoot, "remote-probe", "raw")
		expected = tool.Str(tool.Obj(manifest["remotePhaseFiles"])["remote-probe/raw/raw.jsonl"])
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
		if _, present := metadata["recordingFormat"]; present {
			if _, err := recording.ValidateManifest(data, "raw.jsonl", document.Original); err != nil {
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
