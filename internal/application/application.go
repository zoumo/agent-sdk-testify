// Package application is the single use-case boundary shared by the CLI and
// importable library adapters.
package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/mock"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/record"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type (
	RecordResult = lifecycle.RecordResult
	MockResult   = mock.Result
	Fixture      = lifecycle.Fixture
)

type RecordOptions struct {
	FixtureDir     string
	Connection     string
	URL            string
	TokenFile      string
	Command        string
	NativeArgs     []string
	Timeout        time.Duration
	ConnectTimeout time.Duration
	ResultFD       int
}

type MockOptions struct {
	FixtureDir  string
	Connection  string
	ExecutionID string
	Timeout     time.Duration
	ResultFD    int
	ProgressFD  int
	NativeArgs  []string
}

type PrepareOptions struct {
	FixtureDir       string
	Agent            string
	Case             string
	Plan             string
	Recording        string
	Model            string
	InitialModel     string
	HistorySeed      string
	ProtocolBytes    int64
	StderrBytes      int64
	MaxFrames        int64
	MaxFrameBytes    int64
	EmbeddedTool     bool
	EmbeddedIdentity *version.EmbeddedIdentity
}

type ExportOptions struct {
	FixtureDir string
	Output     string
}

type OpenResult struct {
	Fixture    *Fixture
	Current    *corpus.Material
	FixtureDir string
}

// Streams are caller-owned process streams used by protocol actions.
type Streams struct {
	In  *os.File
	Out *os.File
	Err *os.File
}

// Prepare validates caller input and creates one prepared fixture.
func Prepare(o PrepareOptions) (*Fixture, error) {
	if o.FixtureDir == "" {
		return nil, errors.New("prepare requires --fixture-dir")
	}
	if !filepath.IsAbs(o.FixtureDir) {
		return nil, errors.New("--fixture-dir must be an absolute path")
	}
	if o.ProtocolBytes < 0 || o.StderrBytes < 0 || o.MaxFrames < 0 || o.MaxFrameBytes < 0 {
		return nil, errors.New("prepare limits must be nonnegative")
	}
	if err := requireNewFixtureDir(o.FixtureDir); err != nil {
		return nil, err
	}
	return lifecycle.Prepare(lifecycle.PrepareOptions{
		FixtureDir:       o.FixtureDir,
		Agent:            o.Agent,
		Case:             o.Case,
		Plan:             o.Plan,
		Recording:        o.Recording,
		Model:            o.Model,
		InitialModel:     o.InitialModel,
		HistorySeed:      o.HistorySeed,
		ProtocolBytes:    o.ProtocolBytes,
		StderrBytes:      o.StderrBytes,
		MaxFrames:        o.MaxFrames,
		MaxFrameBytes:    o.MaxFrameBytes,
		EmbeddedTool:     o.EmbeddedTool,
		EmbeddedIdentity: o.EmbeddedIdentity,
	})
}

// Open reads the prepared fixture and its optional current recording.
func Open(fixtureDir string) (*OpenResult, error) {
	if fixtureDir == "" || !filepath.IsAbs(fixtureDir) {
		return nil, errors.New("open requires an absolute fixture directory")
	}
	fixture, dir, err := openPreparedFixture(fixtureDir)
	if err != nil {
		return nil, err
	}
	current, present, err := lifecycle.CurrentArtifact(dir, fixture)
	if err != nil {
		return nil, err
	}
	if !present {
		current = nil
	}
	return &OpenResult{Fixture: fixture, Current: current, FixtureDir: dir}, nil
}

// ExecutionReport regenerates validated evidence for one mock execution.
func ExecutionReport(fixtureDir, executionID string) (*lifecycle.ExecutionReport, error) {
	if fixtureDir == "" || !filepath.IsAbs(fixtureDir) {
		return nil, errors.New("execution report requires an absolute fixture directory")
	}
	return lifecycle.ReadMockExecutionReport(fixtureDir, executionID)
}

// Export validates caller input and copies the current recording.
func Export(o ExportOptions) error {
	if !filepath.IsAbs(o.FixtureDir) || !filepath.IsAbs(o.Output) {
		return errors.New("export requires absolute --fixture-dir and --output paths")
	}
	return lifecycle.Export(o.FixtureDir, o.Output)
}

// Record validates caller input and owns one record action.
func Record(ctx context.Context, o RecordOptions, streams Streams) (*RecordResult, error) {
	if o.URL == "" && len(o.NativeArgs) == 0 {
		return nil, errors.New("local record requires --cmd and native arguments after --")
	}
	if o.URL != "" && (o.Command != "" || len(o.NativeArgs) != 0) {
		return nil, errors.New("remote target excludes --cmd and native arguments")
	}
	if !filepath.IsAbs(o.FixtureDir) {
		return nil, errors.New("--fixture-dir must be absolute")
	}
	if o.Timeout < 0 || o.ConnectTimeout < 0 {
		return nil, errors.New("timeouts must be nonnegative")
	}
	if o.ConnectTimeout > 0 && o.URL == "" {
		return nil, errors.New("connect-timeout requires a remote target")
	}
	if err := validateResultFD(o.ResultFD); err != nil {
		return nil, err
	}
	if streams.In == nil || streams.Out == nil || streams.Err == nil {
		return nil, errors.New("record requires input, output and error streams")
	}
	prepared, err := prepareRecord(o)
	if err != nil {
		return nil, err
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	return record.Run(ctx, prepared, capture.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
}

// Mock validates caller input and owns one mock action.
func Mock(ctx context.Context, o MockOptions, streams Streams) (*MockResult, error) {
	if o.ExecutionID != "" {
		return nil, errors.New("explicit mock execution joins require StartMockAgent")
	}
	if err := validateResultFD(o.ResultFD); err != nil {
		return nil, err
	}
	if o.ResultFD >= 3 && o.ResultFD == o.ProgressFD {
		return nil, errors.New("result-fd and progress-fd must use separate descriptors")
	}
	if o.FixtureDir == "" {
		return nil, errors.New("mock requires --fixture-dir")
	}
	if !filepath.IsAbs(o.FixtureDir) {
		return nil, errors.New("--fixture-dir must be an absolute path")
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
	prepared, err := prepareMock(o)
	if err != nil {
		return nil, err
	}
	return mock.Run(ctx, prepared, streams.In, streams.Out)
}

func validateResultFD(fd int) error {
	if fd != -1 && fd < 3 {
		return errors.New("result-fd must be >=3")
	}
	return nil
}

func requireNewFixtureDir(fixtureDir string) error {
	_, err := os.Lstat(fixtureDir)
	switch {
	case err == nil:
		return fmt.Errorf("--fixture-dir already exists: %s", fixtureDir)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return err
	}
}
