package agenttestify

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/application"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
)

// ToolSource identifies how the library or CLI entered the prepared fixture.
type ToolSource string

const (
	ToolSourceExecutable ToolSource = ToolSource(lifecycle.ToolSourceExecutable)
	ToolSourceEmbedded   ToolSource = ToolSource(lifecycle.ToolSourceEmbedded)
)

// ConnectionTransport distinguishes a launchable process connection from an
// in-process connection with no executable identity.
type ConnectionTransport string

const (
	ConnectionTransportProcess   ConnectionTransport = lifecycle.ConnectionTransportProcess
	ConnectionTransportInProcess ConnectionTransport = lifecycle.ConnectionTransportInProcess
)

// ConnectionStatus is the durable result of one connection in an action
// execution.
type ConnectionStatus string

const (
	ConnectionStatusUnknown ConnectionStatus = ConnectionStatus(lifecycle.ConnectionStatusUnknown)
	ConnectionStatusPending ConnectionStatus = ConnectionStatus(lifecycle.ConnectionStatusPending)
	ConnectionStatusRunning ConnectionStatus = ConnectionStatus(lifecycle.ConnectionStatusRunning)
	ConnectionStatusPassed  ConnectionStatus = ConnectionStatus(lifecycle.ConnectionStatusPassed)
	ConnectionStatusFailed  ConnectionStatus = ConnectionStatus(lifecycle.ConnectionStatusFailed)
)

// ExecutionStatus is the durable result of all declared connections in one
// action execution.
type ExecutionStatus string

const (
	ExecutionStatusUnknown ExecutionStatus = ExecutionStatus(lifecycle.ExecutionStatusUnknown)
	ExecutionStatusRunning ExecutionStatus = ExecutionStatus(lifecycle.ExecutionStatusRunning)
	ExecutionStatusOpen    ExecutionStatus = ExecutionStatus(lifecycle.ExecutionStatusOpen)
	ExecutionStatusPassed  ExecutionStatus = ExecutionStatus(lifecycle.ExecutionStatusPassed)
	ExecutionStatusFailed  ExecutionStatus = ExecutionStatus(lifecycle.ExecutionStatusFailed)
)

// MockTransport distinguishes a process adapter from an in-process mock.
type MockTransport string

const (
	MockTransportInProcess MockTransport = MockTransport(lifecycle.MockTransportInProcess)
	MockTransportProcess   MockTransport = MockTransport(lifecycle.MockTransportProcess)
)

// FailureClass is the machine-readable terminal reason for a mock run.
type FailureClass string

const (
	FailureClassCanceled     FailureClass = FailureClass(lifecycle.FailureClassCanceled)
	FailureClassAborted      FailureClass = FailureClass(lifecycle.FailureClassAborted)
	FailureClassTransport    FailureClass = FailureClass(lifecycle.FailureClassTransport)
	FailureClassStrictReplay FailureClass = FailureClass(lifecycle.FailureClassStrictReplay)
	FailureClassTimeout      FailureClass = FailureClass(lifecycle.FailureClassTimeout)
	FailureClassCleanup      FailureClass = FailureClass(lifecycle.FailureClassCleanup)
	FailureClassInternal     FailureClass = FailureClass(lifecycle.FailureClassInternal)
)

// ActionResult contains the durable execution and connection evidence shared
// by record and mock calls. ConnectionStatus describes this call only;
// ExecutionStatus and ExecutionComplete describe every declared connection.
type ActionResult struct {
	FixtureID          string
	ExecutionID        string
	ConnectionID       string
	ConnectionStatus   ConnectionStatus
	ExecutionStatus    ExecutionStatus
	ExecutionComplete  bool
	Material           *Material
	ExecutionPath      string
	ReceiptPath        string
	ProcessReceiptPath string
	ResultPath         string
}

// PrepareOptions selects the immutable initial state for a new fixture.
type PrepareOptions struct {
	FixtureDir    string
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
}

// ExportOptions names the fixture and a new directory outside it.
type ExportOptions struct {
	FixtureDir string
	Output     string
}

// RecordAgentOptions selects one capture connection and its native or remote target.
type RecordAgentOptions struct {
	Connection     string
	Command        string
	NativeArgs     []string
	URL            string
	TokenFile      string
	Timeout        time.Duration
	ConnectTimeout time.Duration
}

// Streams are caller-owned process streams used by protocol actions.
type Streams struct {
	In  *os.File
	Out *os.File
	Err *os.File
}

// MockAgentOptions selects one replay connection in a prepared fixture.
type MockAgentOptions struct {
	Connection  string
	ExecutionID string
	Timeout     time.Duration
	NativeArgs  []string
}

// MockAgentResult identifies the execution and connection completed by a mock call.
type MockAgentResult struct {
	ActionResult
	Execution *ExecutionReport
}

// StartMockAgentStreams are closeable directional streams transferred to one
// in-process mock run.
type StartMockAgentStreams struct {
	Reader io.ReadCloser
	Writer io.WriteCloser
	Stderr io.Writer
}

// ExecutionStart is the immutable overlay snapshot available before protocol I/O.
type ExecutionStart struct {
	FixtureID       string
	ExecutionID     string
	ConnectionID    string
	Material        Material
	ExecutionSHA256 string
	ExecutionPath   string

	Workspace    string
	ProtocolCwd  string
	Home         string
	Config       string
	Tmp          string
	HistoryFiles map[string]string
	Env          map[string]string
	NativeArgs   []string
	Transport    MockTransport
}

// ExecutionReport is the validated report for every connection in an execution.
type ExecutionReport struct {
	FixtureID       string
	ExecutionID     string
	Status          ExecutionStatus
	Complete        bool
	Material        Material
	ExecutionSHA256 string
	ExecutionPath   string
	Connections     []ConnectionReport
}

// ConnectionReport contains one connection's durable protocol evidence.
type ConnectionReport struct {
	ID     string
	Status ConnectionStatus

	Transport       MockTransport
	FullConsumption bool
	ClientEOF       bool
	NativeLaunched  bool
	CleanupComplete bool
	ExitCode        *int
	Failure         FailureClass
	Error           string

	ReceiptPath        string
	ProcessReceiptPath string
	ResultPath         string
}

// MockRun coordinates one in-process mock connection.
type MockRun struct {
	Start ExecutionStart

	run *application.MockRun
}

// RecordAgentResult identifies the execution and connection completed by a record
// call and, on success, the newly selected material.
type RecordAgentResult struct {
	ActionResult
}

// ToolIdentity describes the code that prepared a fixture.
type ToolIdentity struct {
	Source         ToolSource
	Version        string
	Command        string
	Args           []string
	SHA256         string
	Module         string
	ModuleSum      string
	ModuleRevision string
	ModuleTime     string
	GoVersion      string
}

// Source identifies the initial case, plan or recording.
type Source struct {
	Kind          string
	ID            string
	Path          string
	SHA256        string
	Provenance    string
	DriverProfile string
}

// Limits are the protocol and process limits captured by prepare.
type Limits struct {
	Timeout       time.Duration
	ProtocolBytes int64
	StderrBytes   int64
	MaxFrames     int64
	MaxFrameBytes int64
}

// Connection describes one route connection template.
type Connection struct {
	ID        string
	Transport ConnectionTransport
	Command   string
	Args      []string
	Launcher  string
}

// Route describes one action's isolated host environment.
type Route struct {
	Enabled     bool
	Executor    string
	Target      string
	Provenance  string
	Workspace   string
	ProtocolCwd string
	Env         map[string]string
	RuntimeEnv  []string
	Connections []Connection
}

// Material identifies a current recording without exposing its wire format.
type Material struct {
	ID              string
	Kind            string
	Action          string
	Provenance      string
	Path            string
	PlanSHA256      string
	ManifestSHA256  string
	RecordingSHA256 string
	Connections     []string
	CreatedAt       string
}

// Fixture is a detached, typed view of a prepared test fixture.
type Fixture struct {
	FixtureDir   string
	FixtureID    string
	Tool         ToolIdentity
	AgentType    string
	Source       Source
	HistorySeed  string
	ResourceRoot string
	Workspace    string
	Home         string
	Config       string
	Tmp          string
	ProtocolCwd  string
	Env          map[string]string
	Routes       map[string]Route
	Limits       Limits
	Current      *Material
}

// Prepare creates one fixture using the embedded library tool identity.
func Prepare(options PrepareOptions) (*Fixture, error) {
	_, err := application.Prepare(application.PrepareOptions{
		FixtureDir:    options.FixtureDir,
		Agent:         options.Agent,
		Case:          options.Case,
		Plan:          options.Plan,
		Recording:     options.Recording,
		Model:         options.Model,
		InitialModel:  options.InitialModel,
		HistorySeed:   options.HistorySeed,
		ProtocolBytes: options.ProtocolBytes,
		StderrBytes:   options.StderrBytes,
		MaxFrames:     options.MaxFrames,
		MaxFrameBytes: options.MaxFrameBytes,
		EmbeddedTool:  true,
	})
	if err != nil {
		return nil, err
	}
	result, err := application.Open(options.FixtureDir)
	if err != nil {
		return nil, err
	}
	return fixtureView(result), nil
}

// Open reads a prepared fixture without changing it.
func Open(fixtureDir string) (*Fixture, error) {
	result, err := application.Open(fixtureDir)
	if err != nil {
		return nil, err
	}
	return fixtureView(result), nil
}

// Export copies the current material package without changing the fixture.
func Export(options ExportOptions) error {
	return application.Export(application.ExportOptions{
		FixtureDir: options.FixtureDir,
		Output:     options.Output,
	})
}

// RecordAgent captures one native Agent or remote endpoint through caller-owned streams.
func (f *Fixture) RecordAgent(ctx context.Context, options RecordAgentOptions, streams Streams) (*RecordAgentResult, error) {
	fixtureDir := ""
	if f != nil {
		fixtureDir = f.FixtureDir
	}
	result, err := application.Record(ctx, application.RecordOptions{
		FixtureDir:     fixtureDir,
		Connection:     options.Connection,
		URL:            options.URL,
		TokenFile:      options.TokenFile,
		Command:        options.Command,
		NativeArgs:     slices.Clone(options.NativeArgs),
		Timeout:        options.Timeout,
		ConnectTimeout: options.ConnectTimeout,
		ResultFD:       -1,
	}, application.Streams{
		In:  streams.In,
		Out: streams.Out,
		Err: streams.Err,
	})
	if result == nil {
		return nil, err
	}
	return &RecordAgentResult{
		ActionResult: actionResultView(result.ActionResult),
	}, err
}

// MockAgent replays the current recording through caller-owned streams.
func (f *Fixture) MockAgent(ctx context.Context, options MockAgentOptions, streams Streams) (*MockAgentResult, error) {
	fixtureDir := ""
	if f != nil {
		fixtureDir = f.FixtureDir
	}
	result, err := application.Mock(ctx, application.MockOptions{
		FixtureDir:  fixtureDir,
		Connection:  options.Connection,
		ExecutionID: options.ExecutionID,
		Timeout:     options.Timeout,
		ResultFD:    -1,
		ProgressFD:  -1,
		NativeArgs:  slices.Clone(options.NativeArgs),
	}, application.Streams{
		In:  streams.In,
		Out: streams.Out,
		Err: streams.Err,
	})
	if result == nil {
		return nil, err
	}
	return &MockAgentResult{
		ActionResult: actionResultView(result.ActionResult),
		Execution:    executionReportView(result.Execution),
	}, err
}

// StartMockAgent claims one in-process mock execution and releases replay only
// after the start snapshot is ready.
func (f *Fixture) StartMockAgent(ctx context.Context, options MockAgentOptions, streams StartMockAgentStreams) (*MockRun, error) {
	fixtureDir := ""
	if f != nil {
		fixtureDir = f.FixtureDir
	}
	run, err := application.StartMock(ctx, application.MockOptions{
		FixtureDir:  fixtureDir,
		Connection:  options.Connection,
		ExecutionID: options.ExecutionID,
		Timeout:     options.Timeout,
		NativeArgs:  slices.Clone(options.NativeArgs),
	}, application.StartMockStreams{
		Reader: streams.Reader,
		Writer: streams.Writer,
		Stderr: streams.Stderr,
	})
	if run == nil {
		return nil, err
	}
	return &MockRun{
		Start: executionStartView(run.Start),
		run:   run,
	}, err
}

// FullConsumption reports the one-shot safe point for SDK input half-close.
func (r *MockRun) FullConsumption() <-chan struct{} {
	if r == nil || r.run == nil {
		return nil
	}
	return r.run.FullConsumption()
}

// Done closes after this connection's replay, receipt and finalization end.
func (r *MockRun) Done() <-chan struct{} {
	if r == nil || r.run == nil {
		return nil
	}
	return r.run.Done()
}

// Wait returns the authoritative repeatable terminal result.
func (r *MockRun) Wait(ctx context.Context) (*MockAgentResult, error) {
	if r == nil || r.run == nil {
		return nil, errors.New("mock run is unavailable")
	}
	result, err := r.run.Wait(ctx)
	if result == nil {
		return nil, err
	}
	return &MockAgentResult{
		ActionResult: actionResultView(result.ActionResult),
		Execution:    executionReportView(result.Execution),
	}, err
}

// Abort cancels the run and closes fixture-side streams. It is idempotent.
func (r *MockRun) Abort() error {
	if r == nil || r.run == nil {
		return errors.New("mock run is unavailable")
	}
	return r.run.Abort()
}

func actionResultView(result lifecycle.ActionResult) ActionResult {
	view := ActionResult{
		FixtureID:          result.FixtureID,
		ExecutionID:        result.ExecutionID,
		ConnectionID:       result.ConnectionID,
		ConnectionStatus:   ConnectionStatus(result.ConnectionStatus),
		ExecutionStatus:    ExecutionStatus(result.ExecutionStatus),
		ExecutionComplete:  result.ExecutionComplete,
		ExecutionPath:      result.ExecutionPath,
		ReceiptPath:        result.ReceiptPath,
		ProcessReceiptPath: result.ProcessReceiptPath,
		ResultPath:         result.ResultPath,
	}
	if result.Material != nil {
		view.Material = materialView(*result.Material)
	}
	return view
}

func executionStartView(start application.ExecutionStart) ExecutionStart {
	return ExecutionStart{
		FixtureID:       start.FixtureID,
		ExecutionID:     start.ExecutionID,
		ConnectionID:    start.ConnectionID,
		Material:        *materialView(start.Material),
		ExecutionSHA256: start.ExecutionSHA256,
		ExecutionPath:   start.ExecutionPath,
		Workspace:       start.Workspace,
		ProtocolCwd:     start.ProtocolCwd,
		Home:            start.Home,
		Config:          start.Config,
		Tmp:             start.Tmp,
		HistoryFiles:    maps.Clone(start.HistoryFiles),
		Env:             maps.Clone(start.Env),
		NativeArgs:      slices.Clone(start.NativeArgs),
		Transport:       MockTransport(start.Transport),
	}
}

func executionReportView(report *lifecycle.ExecutionReport) *ExecutionReport {
	if report == nil {
		return nil
	}
	view := &ExecutionReport{
		FixtureID:       report.FixtureID,
		ExecutionID:     report.ExecutionID,
		Status:          ExecutionStatus(report.Status),
		Complete:        report.Complete,
		Material:        *materialView(report.Material),
		ExecutionSHA256: report.ExecutionSHA256,
		ExecutionPath:   report.ExecutionPath,
		Connections:     make([]ConnectionReport, len(report.Connections)),
	}
	for i, connection := range report.Connections {
		var exitCode *int
		if connection.ExitCode != nil {
			code := *connection.ExitCode
			exitCode = &code
		}
		view.Connections[i] = ConnectionReport{
			ID:                 connection.ID,
			Status:             ConnectionStatus(connection.Status),
			Transport:          MockTransport(connection.Transport),
			FullConsumption:    connection.FullConsumption,
			ClientEOF:          connection.ClientEOF,
			NativeLaunched:     connection.NativeLaunched,
			CleanupComplete:    connection.CleanupComplete,
			ExitCode:           exitCode,
			Failure:            FailureClass(connection.Failure),
			Error:              connection.Error,
			ReceiptPath:        connection.ReceiptPath,
			ProcessReceiptPath: connection.ProcessReceiptPath,
			ResultPath:         connection.ResultPath,
		}
	}
	return view
}

func fixtureView(result *application.OpenResult) *Fixture {
	fixture := result.Fixture
	view := &Fixture{
		FixtureDir:   result.FixtureDir,
		FixtureID:    fixture.FixtureID,
		AgentType:    fixture.AgentType,
		HistorySeed:  fixture.HistorySeed,
		ResourceRoot: fixture.ResourceRoot,
		Workspace:    fixture.Workspace,
		Home:         fixture.Home,
		Config:       fixture.Config,
		Tmp:          fixture.Tmp,
		ProtocolCwd:  fixture.ProtocolCwd,
		Env:          maps.Clone(fixture.Env),
		Limits:       limitsView(fixture.Limits),
		Tool: ToolIdentity{
			Source:         ToolSource(fixture.Tool.Source),
			Version:        fixture.Tool.Version,
			Command:        fixture.Tool.Command,
			Args:           slices.Clone(fixture.Tool.Args),
			SHA256:         fixture.Tool.SHA256,
			Module:         fixture.Tool.Module,
			ModuleSum:      fixture.Tool.ModuleSum,
			ModuleRevision: fixture.Tool.ModuleRevision,
			ModuleTime:     fixture.Tool.ModuleTime,
			GoVersion:      fixture.Tool.GoVersion,
		},
		Source: Source{
			Kind:          fixture.Source.Kind,
			ID:            fixture.Source.ID,
			Path:          fixture.Source.Path,
			SHA256:        fixture.Source.SHA256,
			Provenance:    fixture.Source.Provenance,
			DriverProfile: fixture.Source.DriverProfile,
		},
	}
	view.Routes = make(map[string]Route, len(fixture.Routes))
	for name, route := range fixture.Routes {
		view.Routes[name] = routeView(route)
	}
	if result.Current != nil {
		view.Current = materialView(*result.Current)
	}
	return view
}

func routeView(route lifecycle.Route) Route {
	view := Route{
		Enabled:     route.Enabled,
		Executor:    route.Executor,
		Target:      route.Target,
		Provenance:  route.Provenance,
		Workspace:   route.Workspace,
		ProtocolCwd: route.ProtocolCwd,
		Env:         maps.Clone(route.Env),
		RuntimeEnv:  slices.Clone(route.RuntimeEnv),
	}
	view.Connections = make([]Connection, len(route.Connections))
	for i, connection := range route.Connections {
		view.Connections[i] = Connection{
			ID:        connection.ID,
			Transport: ConnectionTransport(connection.Transport),
			Command:   connection.Command,
			Args:      slices.Clone(connection.Args),
			Launcher:  connection.Launcher,
		}
	}
	return view
}

func limitsView(limits lifecycle.Limits) Limits {
	return Limits{
		Timeout:       time.Duration(limits.TimeoutNS),
		ProtocolBytes: limits.ProtocolBytes,
		StderrBytes:   limits.StderrBytes,
		MaxFrames:     limits.MaxFrames,
		MaxFrameBytes: limits.MaxFrameBytes,
	}
}

func materialView(material corpus.Material) *Material {
	return &Material{
		ID:              material.ID,
		Kind:            material.Kind,
		Action:          material.Action,
		Provenance:      material.Provenance,
		Path:            material.Path,
		PlanSHA256:      material.PlanSHA256,
		ManifestSHA256:  material.ManifestSHA256,
		RecordingSHA256: material.RecordingSHA256,
		Connections:     slices.Clone(material.Connections),
		CreatedAt:       material.CreatedAt,
	}
}
