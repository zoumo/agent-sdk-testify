// Package application is the single use-case boundary shared by the CLI and
// importable library adapters.
package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/mock"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/record"
)

type (
	RecordOptions = record.Options
	MockOptions   = mock.Options
	MockResult    = mock.Result
	Session       = lifecycle.Session
)

type PrepareOptions struct {
	SessionDir    string
	Agent         string
	Case          string
	Plan          string
	Recording     string
	Model         string
	InitialModel  string
	HistorySeed   string
	ProtocolBytes int64
	StderrBytes   int64
	MaxFrames     int64
	MaxFrameBytes int64
	EmbeddedTool  bool
}

type ExportOptions struct {
	SessionDir string
	Output     string
}

type OpenResult struct {
	Session *Session
	Current *corpus.Material
	Dir     string
}

// Streams are caller-owned process streams used by protocol actions.
type Streams struct {
	In  *os.File
	Out *os.File
	Err *os.File
}

// Prepare validates caller input and creates one prepared session.
func Prepare(o PrepareOptions) (*Session, error) {
	if o.SessionDir == "" {
		return nil, errors.New("prepare requires --session-dir")
	}
	if !filepath.IsAbs(o.SessionDir) {
		return nil, errors.New("--session-dir must be an absolute path")
	}
	if o.ProtocolBytes < 0 || o.StderrBytes < 0 || o.MaxFrames < 0 || o.MaxFrameBytes < 0 {
		return nil, errors.New("prepare limits must be nonnegative")
	}
	if err := requireNewSessionDir(o.SessionDir); err != nil {
		return nil, err
	}
	return lifecycle.Prepare(lifecycle.PrepareOptions{
		SessionDir:    o.SessionDir,
		Agent:         o.Agent,
		Case:          o.Case,
		Plan:          o.Plan,
		Recording:     o.Recording,
		Model:         o.Model,
		InitialModel:  o.InitialModel,
		HistorySeed:   o.HistorySeed,
		ProtocolBytes: o.ProtocolBytes,
		StderrBytes:   o.StderrBytes,
		MaxFrames:     o.MaxFrames,
		MaxFrameBytes: o.MaxFrameBytes,
		EmbeddedTool:  o.EmbeddedTool,
	})
}

// Open reads the prepared session and its optional current recording.
func Open(sessionDir string) (*OpenResult, error) {
	if sessionDir == "" || !filepath.IsAbs(sessionDir) {
		return nil, errors.New("open requires an absolute session directory")
	}
	session, err := lifecycle.OpenSession(sessionDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(session.ResourceRoot)
	current, present, err := lifecycle.CurrentArtifact(dir, session)
	if err != nil {
		return nil, err
	}
	if !present {
		current = nil
	}
	return &OpenResult{Session: session, Current: current, Dir: dir}, nil
}

// Export validates caller input and copies the current recording.
func Export(o ExportOptions) error {
	if !filepath.IsAbs(o.SessionDir) || !filepath.IsAbs(o.Output) {
		return errors.New("export requires absolute --session-dir and --output paths")
	}
	return lifecycle.Export(o.SessionDir, o.Output)
}

// Record validates caller input and owns one record action.
func Record(ctx context.Context, o RecordOptions, streams Streams) error {
	if o.URL == "" && len(o.NativeArgs) == 0 {
		return errors.New("local record requires --cmd and native arguments after --")
	}
	if o.URL != "" && (o.Command != "" || len(o.NativeArgs) != 0) {
		return errors.New("remote target excludes --cmd and native arguments")
	}
	if !filepath.IsAbs(o.SessionDir) {
		return errors.New("--session-dir must be absolute")
	}
	if o.Timeout < 0 || o.ConnectTimeout < 0 {
		return errors.New("timeouts must be nonnegative")
	}
	if o.ConnectTimeout > 0 && o.URL == "" {
		return errors.New("connect-timeout requires a remote target")
	}
	if err := validateResultFD(o.ResultFD); err != nil {
		return err
	}
	if streams.In == nil || streams.Out == nil || streams.Err == nil {
		return errors.New("record requires input, output and error streams")
	}
	if o.Connection == "" {
		o.Connection = "main"
	}
	return record.Run(ctx, o, capture.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
}

// Mock validates caller input and owns one mock action.
func Mock(ctx context.Context, o MockOptions, streams Streams) (*MockResult, error) {
	if err := validateResultFD(o.ResultFD); err != nil {
		return nil, err
	}
	if o.ResultFD >= 3 && o.ResultFD == o.ProgressFD {
		return nil, errors.New("result-fd and progress-fd must use separate descriptors")
	}
	if o.SessionDir == "" {
		return nil, errors.New("mock requires --session-dir")
	}
	if !filepath.IsAbs(o.SessionDir) {
		return nil, errors.New("--session-dir must be an absolute path")
	}
	if o.Timeout < 0 {
		return nil, errors.New("mock timeout must be nonnegative")
	}
	if o.ProgressFD != -1 && o.ProgressFD < 3 {
		return nil, errors.New("progress-fd must be >=3")
	}
	if streams.In == nil || streams.Out == nil || streams.Err == nil {
		return nil, errors.New("mock requires input, output and error streams")
	}
	if o.Connection == "" {
		o.Connection = "main"
	}
	return mock.Run(ctx, o, streams.In, streams.Out)
}

func validateResultFD(fd int) error {
	if fd != -1 && fd < 3 {
		return errors.New("result-fd must be >=3")
	}
	return nil
}

func requireNewSessionDir(sessionDir string) error {
	_, err := os.Lstat(sessionDir)
	switch {
	case err == nil:
		return fmt.Errorf("--session-dir already exists: %s", sessionDir)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return err
	}
}
