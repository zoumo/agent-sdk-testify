package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	protocolcmd "github.com/zoumo/agent-sdk-testify/protocol/command"
	"github.com/zoumo/agent-sdk-testify/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/protocol/replay"
)

// Direct mock owns only its protocol descriptors and persistent receipt.
func executeMock(ctx context.Context, args []string) int {
	err := mockBuiltinMode(ctx, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-sdk-testify mock:", err)
	}
	return protocolcmd.ExitCode(err)
}

func mockExternal(ctx context.Context, args []string) error {
	return mockExternalSelected(ctx, args, nil)
}

func mockExternalSelected(ctx context.Context, args []string, selection *builtinSelection) (err error) {
	f := flag.NewFlagSet("mock", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	root := f.String("recording", "", "Absolute external capture directory")
	provider := f.String("provider", "", "codex or claude (default: plan provider)")
	report := f.String("report", "", "New persistent receipt file")
	seconds := f.Float64("timeout", 0, "Seconds; default: scenario timeout, then provider default")
	phase := f.String("phase", "main", "Declared stream: main or probe")
	progress := f.Int("progress-fd", -1, "Inherited full-consumption notification descriptor (>=3)")
	help := f.Bool("help", false, "Show mock usage")
	var flags, native []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			native = append(native, args[i+1:]...)
			break
		}
		if arg == "-h" {
			arg = "--help"
		}
		name, _, equals := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !strings.HasPrefix(arg, "--") || f.Lookup(name) == nil {
			native = append(native, arg)
			continue
		}
		flags = append(flags, arg)
		if !equals && name != "help" {
			if i+1 == len(args) {
				return protocolcmd.Fail(protocolcmd.ExitConfiguration, fmt.Errorf("missing value for --%s", name))
			}
			i++
			flags = append(flags, args[i])
		}
	}
	if e := f.Parse(flags); e != nil {
		return protocolcmd.Fail(protocolcmd.ExitConfiguration, e)
	}
	if *help {
		fmt.Fprintln(os.Stderr, "Builtin: mock --list [--provider PROVIDER] --json; mock --describe --provider PROVIDER --case CASE --json; mock [--prepare] --provider PROVIDER --case CASE (see docs/builtin.md)")
		fmt.Fprintln(os.Stderr, "Prepared modes: mock --prepare --recording ABS --output ABS; mock --run ABS/run.json [--connection main|probe]; mock --finish ABS/run.json")
		f.PrintDefaults()
		return nil
	}
	bad := func(e error) error { return protocolcmd.Fail(protocolcmd.ExitConfiguration, e) }
	if !filepath.IsAbs(*root) || *report == "" {
		return bad(errors.New("--recording /absolute/capture and --report FILE are required"))
	}
	if math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds < 0 || *seconds >= float64(math.MaxInt64)/float64(time.Second) || (*seconds > 0 && time.Duration(*seconds*float64(time.Second)) == 0) {
		return bad(errors.New("invalid mock timeout"))
	}
	// Resolve parents before opening so report aliases cannot overwrite evidence.
	if *progress != -1 && *progress < 3 {
		return bad(errors.New("progress-fd must be >=3"))
	}
	var notification *os.File
	if *progress >= 3 {
		fd, e := syscall.Dup(*progress)
		if e != nil {
			return bad(e)
		}
		syscall.CloseOnExec(fd)
		notification = os.NewFile(uintptr(fd), "consumption-notification")
		defer notification.Close()
	}
	sourceRoot, e := filepath.EvalSymlinks(*root)
	if e != nil {
		return bad(e)
	}
	reportPath, e := filepath.Abs(*report)
	if e != nil {
		return bad(e)
	}
	reportParent, e := filepath.EvalSymlinks(filepath.Dir(reportPath))
	if e != nil {
		return bad(e)
	}
	reportPath = filepath.Join(reportParent, filepath.Base(reportPath))
	if rel, e := filepath.Rel(sourceRoot, reportPath); e != nil || filepath.IsLocal(rel) {
		return bad(errors.New("report must be outside the recording directory"))
	}
	receiptFile, e := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return bad(e)
	}
	receipt := object{"schemaVersion": 1, "version": 1, "status": "failed", "peer": "Go native mock", "nativeLaunched": false,
		"fullConsumption": false, "clientEOF": false, "cleanupComplete": true, "recording": sourceRoot, "toolVersion": protocolcmd.Version}
	defer func() {
		if selection != nil {
			receipt["builtin"] = selection
			cleanupErr := os.RemoveAll(sourceRoot)
			receipt["cleanupComplete"] = cleanupErr == nil
			err = errors.Join(err, cleanupErr)
		}
		receipt["exitCode"] = protocolcmd.ExitCode(err)
		if err != nil {
			receipt["error"] = err.Error()
		}
		closeErr := receiptFile.Close()
		err = errors.Join(err, closeErr, atomicMockJSON(reportPath, receipt))
	}()
	if selection != nil {
		if e = checkBuiltinMaterial(sourceRoot, selection.Material); e != nil {
			return bad(e)
		}
	}
	manifest, plan, document, e := loadMockPhase(sourceRoot, *provider, *phase)
	if e != nil {
		return bad(e)
	}
	*provider = str(plan["provider"])
	if e = protocolcmd.ValidateNativeArgs(*provider, native); e != nil {
		return bad(e)
	}
	receipt["provider"] = *provider
	receipt["planSHA256"] = manifest["planSHA256"]
	receipt["provenance"] = manifest["provenance"]
	receipt["recordingSHA256"] = document.SHA256
	receipt["frames"] = len(document.Frames)
	timeout := time.Duration(*seconds * float64(time.Second))
	if timeout == 0 {
		timeout = document.Timeout
	}
	if timeout == 0 {
		timeout = 30 * time.Second
		if *provider == "claude" {
			timeout = 10 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	consumed, e := mockProtocolNotifying(ctx, document, notification)
	receipt["fullConsumption"] = consumed
	if e != nil {
		code := protocolcmd.ExitFailure
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			code = protocolcmd.ExitCanceled
		} else if errors.Is(e, replay.ErrDisconnected) {
			code = protocolcmd.ExitDisconnect
		}
		return protocolcmd.Fail(code, e)
	}
	receipt["status"], receipt["fullConsumption"], receipt["clientEOF"] = "passed", true, true
	return nil
}

// The engine's dedicated progress descriptor distinguishes full consumption
// from successful EOF. Run closes it and all protocol duplicates before return.
func mockProtocol(ctx context.Context, document *recording.Document) (bool, error) {
	return mockProtocolNotifying(ctx, document, nil)
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

// loadMockSource is shared by direct and prepared mock. It requires the same
// immutable plan/raw bindings and format validation at both entry points.
func loadMockSource(sourceRoot, provider string) (object, object, *recording.Document, error) {
	return loadMockPhase(sourceRoot, provider, "main")
}

func loadMockPhase(sourceRoot, provider, phase string) (object, object, *recording.Document, error) {
	var e error
	// Unlike legacy shared-material loaders, this entry requires the source
	// manifest to bind both the plan and the raw stream, even for synthetic input.
	var manifest, plan object
	if e := readJSON(filepath.Join(sourceRoot, "manifest.json"), &manifest); e != nil {
		return nil, nil, nil, e
	}
	if str(manifest["planSHA256"]) == "" || str(obj(manifest["files"])["raw.jsonl"]) == "" {
		return nil, nil, nil, errors.New("source manifest requires planSHA256 and files.raw.jsonl")
	}
	if e = verifySourcePhase(sourceRoot, true); e != nil {
		return nil, nil, nil, e
	}
	if e = readJSON(filepath.Join(sourceRoot, "plan.json"), &plan); e != nil {
		return nil, nil, nil, e
	}
	if provider == "" {
		provider = str(plan["provider"])
	} else if str(plan["provider"]) != "" && provider != str(plan["provider"]) {
		return nil, nil, nil, errors.New("provider differs from selected recording")
	}
	rawRoot := filepath.Join(sourceRoot, "raw")
	expected := str(obj(manifest["files"])["raw.jsonl"])
	if phase == "probe" && plan["driverProfile"] == "remote-close" {
		rawRoot = filepath.Join(sourceRoot, "remote-probe/raw")
		expected = str(obj(manifest["remotePhaseFiles"])["remote-probe/raw/raw.jsonl"])
		if expected == "" {
			return nil, nil, nil, errors.New("source manifest must bind selected probe stream")
		}
		if e = verifySourcePhase(filepath.Join(sourceRoot, "remote-probe"), true); e != nil {
			return nil, nil, nil, e
		}
	} else if phase != "main" {
		return nil, nil, nil, errors.New("undeclared recording phase")
	}
	source, e := os.Open(filepath.Join(rawRoot, "raw.jsonl"))
	if e != nil {
		return nil, nil, nil, e
	}
	document, readErr := recording.Read(source, provider, recording.Limits{})
	if e = errors.Join(readErr, source.Close()); e != nil {
		return nil, nil, nil, e
	}
	if document.SHA256 != expected {
		return nil, nil, nil, errors.New("source hash mismatch: raw.jsonl")
	}
	// Legacy manifests have no format descriptor. If one is supplied, enforce
	// its version and transport rather than treating it as decorative metadata.
	for _, name := range []string{filepath.Join(sourceRoot, "manifest.json"), filepath.Join(rawRoot, "manifest.json")} {
		data, readErr := os.ReadFile(name)
		if os.IsNotExist(readErr) {
			continue
		}
		var metadata object
		if readErr != nil {
			return nil, nil, nil, readErr
		}
		if e = json.Unmarshal(data, &metadata); e != nil {
			return nil, nil, nil, e
		}
		if _, present := metadata["recordingFormat"]; present {
			if _, e = recording.ValidateManifest(data, "raw.jsonl", document.Original); e != nil {
				return nil, nil, nil, e
			}
		}
	}
	if provider != "codex" && provider != "claude" {
		return nil, nil, nil, errors.New("materials must identify codex or claude")
	}
	return manifest, plan, document, nil
}
