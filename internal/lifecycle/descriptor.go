// Package lifecycle owns fixture identity, environment routes,
// execution descriptors and finalization. Protocol replay and native process
// behavior stay in their existing packages.
package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

const SchemaVersion = 7

const (
	ToolSourceExecutable ToolSource = "executable"
	ToolSourceEmbedded   ToolSource = "embedded"

	ConnectionTransportProcess   = "process"
	ConnectionTransportInProcess = "in-process"
)

type Command struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type ToolSource string

// ToolIdentity is executable or embedded identity. A machine-local executable
// path is resolved from the running tool and verified against the recorded
// SHA-256; it is never part of the persisted control plane.
type ToolIdentity struct {
	Source         ToolSource `json:"source"`
	Version        string     `json:"version"`
	Command        string     `json:"-"`
	Args           []string   `json:"-"`
	SHA256         string     `json:"sha256,omitempty"`
	Module         string     `json:"module,omitempty"`
	ModuleSum      string     `json:"moduleSum,omitempty"`
	ModuleRevision string     `json:"moduleRevision,omitempty"`
	ModuleTime     string     `json:"moduleTime,omitempty"`
	GoVersion      string     `json:"goVersion,omitempty"`
}

type toolIdentityWire struct {
	Source         ToolSource `json:"source"`
	Version        string     `json:"version"`
	SHA256         string     `json:"sha256,omitempty"`
	Module         string     `json:"module,omitempty"`
	ModuleSum      string     `json:"moduleSum,omitempty"`
	ModuleRevision string     `json:"moduleRevision,omitempty"`
	ModuleTime     string     `json:"moduleTime,omitempty"`
	GoVersion      string     `json:"goVersion,omitempty"`
}

func (t *ToolIdentity) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"command", "args", "argv"} {
		if _, present := fields[name]; present {
			return fmt.Errorf("schema %d tool identity omits the machine-local %q field", SchemaVersion, name)
		}
	}
	var wire toolIdentityWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*t = ToolIdentity{
		Source: wire.Source, Version: wire.Version, SHA256: wire.SHA256,
		Module: wire.Module, ModuleSum: wire.ModuleSum,
		ModuleRevision: wire.ModuleRevision, ModuleTime: wire.ModuleTime,
		GoVersion: wire.GoVersion,
	}
	return nil
}

func (t ToolIdentity) MarshalJSON() ([]byte, error) {
	return json.Marshal(toolIdentityWire{
		Source: t.Source, Version: t.Version, SHA256: t.SHA256,
		Module: t.Module, ModuleSum: t.ModuleSum,
		ModuleRevision: t.ModuleRevision, ModuleTime: t.ModuleTime,
		GoVersion: t.GoVersion,
	})
}

// Resolve derives the tool's executable path and verifies that it still matches
// the recorded identity. A CLI records its runtime launch binding so a library
// host can still open the fixture with the exact recorded binary.
func (t *ToolIdentity) Resolve(fixtureDir string) error {
	switch t.Source {
	case ToolSourceExecutable:
		command, _, hash, selfErr := SelfCommand()
		if selfErr == nil && hash == t.SHA256 {
			t.Command = command.Command
			t.Args = nil
			return nil
		}
		binding, err := readToolBinding(fixtureDir)
		if err != nil {
			return errors.New("tool executable differs from its recorded identity")
		}
		if t.SHA256 == "" || !filepath.IsAbs(binding.Command) {
			return errors.New("tool executable differs from its recorded identity")
		}
		bindingHash, err := tool.FileDigest(binding.Command)
		if err != nil || bindingHash != t.SHA256 {
			return errors.New("tool executable differs from its recorded identity")
		}
		t.Command = binding.Command
		t.Args = slices.Clone(binding.Args)
	case ToolSourceEmbedded:
	default:
		return fmt.Errorf("unsupported tool source %q", t.Source)
	}
	return nil
}

func readToolBinding(fixtureDir string) (Command, error) {
	var binding Command
	if err := tool.ReadJSON(ToolBindingPath(fixtureDir), &binding); err != nil {
		return Command{}, err
	}
	if binding.Command == "" {
		return Command{}, errors.New("tool binding omits its command")
	}
	return binding, nil
}

func (t ToolIdentity) Validate() error {
	switch t.Source {
	case ToolSourceExecutable:
		if t.Version == "" || t.Command == "" || !filepath.IsAbs(t.Command) || t.SHA256 == "" {
			return errors.New("executable identity requires absolute command, version, and SHA-256")
		}
		if digest, err := hex.DecodeString(t.SHA256); err != nil || len(digest) != sha256.Size {
			return errors.New("executable identity SHA-256 is invalid")
		}
		if t.Module != "" || t.ModuleSum != "" || t.ModuleRevision != "" || t.ModuleTime != "" || t.GoVersion != "" {
			return errors.New("executable identity declares embedded module fields")
		}
	case ToolSourceEmbedded:
		if t.Command != "" || len(t.Args) != 0 || t.SHA256 != "" {
			return errors.New("embedded identity declares executable fields")
		}
		if t.Module != version.Module {
			return fmt.Errorf("embedded identity module must be %s", version.Module)
		}
		if t.GoVersion == "" {
			return errors.New("embedded identity requires the Go version")
		}
		if t.Version == "(devel)" && t.ModuleRevision == "" {
			return errors.New("embedded development identity requires an exact module revision")
		}
		if t.Version != "(devel)" && !version.ExactModuleVersion(t.Version) {
			return fmt.Errorf("embedded identity version %q is not exact", t.Version)
		}
		if t.ModuleSum != "" && !version.ValidModuleSum(t.ModuleSum) {
			return errors.New("embedded identity module sum is invalid")
		}
		if t.ModuleRevision != "" && !version.ValidRevision(t.ModuleRevision) {
			return errors.New("embedded identity module revision is invalid")
		}
		if t.ModuleTime != "" && !version.ValidCommitTime(t.ModuleTime) {
			return errors.New("embedded identity module time is invalid")
		}
		if t.ModuleRevision == "" && t.ModuleSum == "" {
			return errors.New("embedded identity has neither an exact revision nor a module sum")
		}
	default:
		return fmt.Errorf("unsupported tool source %q", t.Source)
	}
	return nil
}

func (t ToolIdentity) bindSHA256(evidence tool.Object) {
	if t.SHA256 != "" {
		evidence["toolSHA256"] = t.SHA256
	}
}

type Source struct {
	Kind          string `json:"kind"`
	ID            string `json:"id,omitempty"`
	Path          string `json:"path,omitempty"`
	SHA256        string `json:"sha256"`
	Provenance    string `json:"provenance"`
	DriverProfile string `json:"driverProfile,omitempty"`
}

type Limits struct {
	TimeoutNS     int64 `json:"timeoutNs,omitempty"`
	ProtocolBytes int64 `json:"protocolBytes,omitempty"`
	StderrBytes   int64 `json:"stderrBytes,omitempty"`
	MaxFrames     int64 `json:"maxFrames,omitempty"`
	MaxFrameBytes int64 `json:"maxFrameBytes,omitempty"`
}

type ConnectionTemplate struct {
	ID        string   `json:"id"`
	Transport string   `json:"transport"`
	Command   string   `json:"-"`
	Args      []string `json:"-"`
	Launcher  string   `json:"-"`
}

func (c *ConnectionTemplate) UnmarshalJSON(data []byte) error {
	type connectionTemplate ConnectionTemplate
	var decoded connectionTemplate
	fields, err := decodeConnectionObject(data, &decoded)
	if err != nil {
		return err
	}
	if err := validateConnectionFields(fields, decoded.Transport, "id", "transport"); err != nil {
		return err
	}
	*c = ConnectionTemplate(decoded)
	return nil
}

type Route struct {
	Enabled     bool                 `json:"enabled"`
	Executor    string               `json:"executor"`
	Target      string               `json:"target"`
	Provenance  string               `json:"provenance"`
	Workspace   string               `json:"-"`
	ProtocolCwd string               `json:"protocolCwd,omitempty"`
	Env         map[string]string    `json:"-"`
	RuntimeEnv  []string             `json:"runtimeEnv,omitempty"`
	Connections []ConnectionTemplate `json:"connections"`
}

type Fixture struct {
	SchemaVersion    int               `json:"schemaVersion"`
	FixtureID        string            `json:"fixtureId"`
	Tool             ToolIdentity      `json:"tool"`
	AgentType        string            `json:"agentType"`
	Source           Source            `json:"source"`
	HistorySeed      string            `json:"-"`
	HistorySeedFiles map[string]string `json:"historySeedFiles,omitempty"`
	ResourceRoot     string            `json:"-"`
	Workspace        string            `json:"-"`
	Home             string            `json:"-"`
	Config           string            `json:"-"`
	Tmp              string            `json:"-"`
	ProtocolCwd      string            `json:"protocolCwd,omitempty"`
	Env              map[string]string `json:"-"`
	Routes           map[string]Route  `json:"routes"`
	Limits           Limits            `json:"limits"`

	dir string
}

type fixtureWire struct {
	SchemaVersion    int                  `json:"schemaVersion"`
	FixtureID        string               `json:"fixtureId"`
	Tool             ToolIdentity         `json:"tool"`
	AgentType        string               `json:"agentType"`
	Source           Source               `json:"source"`
	HistorySeed      string               `json:"historySeed,omitempty"`
	HistorySeedFiles map[string]string    `json:"historySeedFiles,omitempty"`
	ProtocolCwd      string               `json:"protocolCwd,omitempty"`
	Routes           map[string]routeWire `json:"routes"`
	Limits           Limits               `json:"limits"`
}

type routeWire struct {
	Enabled     bool             `json:"enabled"`
	Executor    string           `json:"executor"`
	Target      string           `json:"target"`
	Provenance  string           `json:"provenance"`
	ProtocolCwd string           `json:"protocolCwd,omitempty"`
	RuntimeEnv  []string         `json:"runtimeEnv,omitempty"`
	Connections []connectionWire `json:"connections"`
}

type connectionWire struct {
	ID        string `json:"id"`
	Transport string `json:"transport"`
}

func (f Fixture) MarshalJSON() ([]byte, error) {
	if f.dir == "" {
		return nil, errors.New("fixture descriptor has no root")
	}
	source := f.Source
	reference, err := FixtureReference(f.dir, source.Path)
	if err != nil {
		return nil, fmt.Errorf("fixture source: %w", err)
	}
	source.Path = reference
	seed := ""
	if f.HistorySeed != "" {
		seed, err = FixtureReference(f.dir, f.HistorySeed)
		if err != nil {
			return nil, fmt.Errorf("fixture history seed: %w", err)
		}
	}
	protocolCwd := ""
	if f.ProtocolCwd != f.Workspace {
		protocolCwd = f.ProtocolCwd
	}
	routes := make(map[string]routeWire, len(f.Routes))
	for action, route := range f.Routes {
		routeCwd := ""
		if route.ProtocolCwd != route.Workspace {
			routeCwd = route.ProtocolCwd
		}
		connections := make([]connectionWire, len(route.Connections))
		for i, connection := range route.Connections {
			connections[i] = connectionWire{ID: connection.ID, Transport: connection.Transport}
		}
		routes[action] = routeWire{
			Enabled: route.Enabled, Executor: route.Executor, Target: route.Target,
			Provenance: route.Provenance, ProtocolCwd: routeCwd,
			RuntimeEnv: slices.Clone(route.RuntimeEnv), Connections: connections,
		}
	}
	return json.Marshal(fixtureWire{
		SchemaVersion: f.SchemaVersion, FixtureID: f.FixtureID, Tool: f.Tool,
		AgentType: f.AgentType, Source: source, HistorySeed: seed,
		HistorySeedFiles: f.HistorySeedFiles, ProtocolCwd: protocolCwd,
		Routes: routes, Limits: f.Limits,
	})
}

func (f *Fixture) UnmarshalJSON(data []byte) error {
	var wire fixtureWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	routes := make(map[string]Route, len(wire.Routes))
	for action, route := range wire.Routes {
		connections := make([]ConnectionTemplate, len(route.Connections))
		for i, connection := range route.Connections {
			connections[i] = ConnectionTemplate{ID: connection.ID, Transport: connection.Transport}
		}
		routes[action] = Route{
			Enabled: route.Enabled, Executor: route.Executor, Target: route.Target,
			Provenance: route.Provenance, ProtocolCwd: route.ProtocolCwd,
			RuntimeEnv: slices.Clone(route.RuntimeEnv), Connections: connections,
		}
	}
	*f = Fixture{
		SchemaVersion: wire.SchemaVersion, FixtureID: wire.FixtureID, Tool: wire.Tool,
		AgentType: wire.AgentType, Source: wire.Source, HistorySeed: wire.HistorySeed,
		HistorySeedFiles: wire.HistorySeedFiles, ProtocolCwd: wire.ProtocolCwd,
		Routes: routes, Limits: wire.Limits,
	}
	return nil
}

// Resolve derives every fixed-layout location from the caller-supplied fixture
// root and validates persisted relative references.
func (f *Fixture) Resolve(fixtureDir string) error {
	f.dir = fixtureDir
	f.ResourceRoot = ResourceRoot(fixtureDir)
	f.Workspace = BaselineWorkspace(fixtureDir)
	baseline := BaselineRoot(fixtureDir)
	f.Home = filepath.Join(baseline, "home")
	f.Config = filepath.Join(baseline, "config")
	f.Tmp = filepath.Join(baseline, "tmp")
	if err := f.Tool.Resolve(fixtureDir); err != nil {
		return err
	}
	if f.Source.Path == "" {
		return errors.New("fixture source omits its reference")
	}
	sourcePath, err := ResolveFixtureReference(fixtureDir, f.Source.Path)
	if err != nil {
		return fmt.Errorf("fixture source: %w", err)
	}
	f.Source.Path = sourcePath
	if f.HistorySeed != "" {
		seed, err := ResolveFixtureReference(fixtureDir, f.HistorySeed)
		if err != nil {
			return fmt.Errorf("fixture history seed: %w", err)
		}
		f.HistorySeed = seed
	}
	f.Env = baseEnvironment(f.AgentType, baseline)
	for action := range f.Routes {
		route, err := ResolveRoute(f, action)
		if err != nil {
			return err
		}
		f.Routes[action] = route
	}
	if f.ProtocolCwd == "" {
		f.ProtocolCwd = f.Workspace
	}
	return nil
}

// connectionLaunch derives the framework-owned argv and launcher for a declared
// process connection. Recorded native arguments remain outside this contract.
func connectionLaunch(fixtureDir, action, id string) ([]string, string) {
	switch action {
	case "record":
		return []string{"agent", "record", "--fixture-dir", fixtureDir, "--cmd"}, ""
	case "mock":
		return []string{"agent", "mock", "--fixture-dir", fixtureDir, "--connection", id}, LauncherPath(fixtureDir, id)
	default:
		return nil, ""
	}
}

type ExecutionConnection struct {
	ID              string   `json:"id"`
	Transport       string   `json:"transport"`
	Command         string   `json:"-"`
	Args            []string `json:"-"`
	Launcher        string   `json:"-"`
	Material        string   `json:"-"`
	MaterialID      string   `json:"materialId,omitempty"`
	PlanSHA256      string   `json:"planSHA256,omitempty"`
	ManifestSHA256  string   `json:"manifestSHA256,omitempty"`
	RecordingSHA256 string   `json:"recordingSHA256,omitempty"`
	Receipt         string   `json:"receipt"`
	ProcessReceipt  string   `json:"processReceipt,omitempty"`
}

func (c *ExecutionConnection) UnmarshalJSON(data []byte) error {
	type executionConnection ExecutionConnection
	var decoded executionConnection
	fields, err := decodeConnectionObject(data, &decoded)
	if err != nil {
		return err
	}
	if err := validateConnectionFields(fields, decoded.Transport,
		"id", "transport", "materialId", "planSHA256", "manifestSHA256",
		"recordingSHA256", "receipt", "processReceipt",
	); err != nil {
		return err
	}
	*c = ExecutionConnection(decoded)
	return nil
}

type Admission struct {
	Index     string `json:"index"`
	Scenarios string `json:"scenarios"`
	Archive   string `json:"archive"`
}

type Execution struct {
	SchemaVersion int                   `json:"schemaVersion"`
	ExecutionID   string                `json:"executionId"`
	FixtureID     string                `json:"fixtureId"`
	Command       string                `json:"command"`
	Args          []string              `json:"args,omitempty"`
	NativeArgs    []string              `json:"nativeArgs,omitempty"`
	Source        Source                `json:"source"`
	Executor      string                `json:"executor"`
	Target        string                `json:"target"`
	Provenance    string                `json:"provenance"`
	Workspace     string                `json:"-"`
	ProtocolCwd   string                `json:"protocolCwd,omitempty"`
	Home          string                `json:"-"`
	Config        string                `json:"-"`
	Tmp           string                `json:"-"`
	HistorySeed   string                `json:"-"`
	HistoryFiles  map[string]string     `json:"historyFiles,omitempty"`
	Env           map[string]string     `json:"-"`
	RuntimeEnv    map[string]string     `json:"-"`
	Connections   []ExecutionConnection `json:"connections"`
	ArtifactRoot  string                `json:"-"`
	Artifact      corpus.Material       `json:"artifact"`
	Admission     *Admission            `json:"admission,omitempty"`
	Limits        Limits                `json:"limits"`
	ProgressFD    int                   `json:"progressFd,omitempty"`
	StartedAt     string                `json:"startedAt"`

	dir string
}

type executionWire struct {
	SchemaVersion int                   `json:"schemaVersion"`
	ExecutionID   string                `json:"executionId"`
	FixtureID     string                `json:"fixtureId"`
	Command       string                `json:"command"`
	Args          []string              `json:"args,omitempty"`
	NativeArgs    []string              `json:"nativeArgs,omitempty"`
	Source        Source                `json:"source"`
	Executor      string                `json:"executor"`
	Target        string                `json:"target"`
	Provenance    string                `json:"provenance"`
	ProtocolCwd   string                `json:"protocolCwd,omitempty"`
	HistoryFiles  map[string]string     `json:"historyFiles,omitempty"`
	Connections   []ExecutionConnection `json:"connections"`
	Artifact      corpus.Material       `json:"artifact"`
	Admission     *Admission            `json:"admission,omitempty"`
	Limits        Limits                `json:"limits"`
	ProgressFD    int                   `json:"progressFd,omitempty"`
	StartedAt     string                `json:"startedAt"`
}

func (e Execution) MarshalJSON() ([]byte, error) {
	if e.dir == "" {
		return nil, errors.New("execution descriptor has no fixture root")
	}
	args, err := stripFixtureArgs(e.dir, e.Args)
	if err != nil {
		return nil, err
	}
	source := e.Source
	sourcePath, err := FixtureReference(e.dir, source.Path)
	if err != nil {
		return nil, err
	}
	source.Path = sourcePath
	protocolCwd := ""
	if e.ProtocolCwd != e.Workspace {
		protocolCwd = e.ProtocolCwd
	}
	connections := make([]ExecutionConnection, len(e.Connections))
	for i, connection := range e.Connections {
		if err := validateExecutionID(connection.ID); err != nil {
			return nil, err
		}
		receipt, err := PackageReference(e.dir, e.Artifact.ID, connection.Receipt)
		if err != nil {
			return nil, fmt.Errorf("connection %s receipt: %w", connection.ID, err)
		}
		connection.Receipt = receipt
		if connection.ProcessReceipt != "" {
			process, err := PackageReference(e.dir, e.Artifact.ID, connection.ProcessReceipt)
			if err != nil {
				return nil, fmt.Errorf("connection %s process receipt: %w", connection.ID, err)
			}
			connection.ProcessReceipt = process
		}
		connection.Command, connection.Args, connection.Launcher, connection.Material = "", nil, "", ""
		connections[i] = connection
	}
	return json.Marshal(executionWire{
		SchemaVersion: e.SchemaVersion, ExecutionID: e.ExecutionID, FixtureID: e.FixtureID,
		Command: e.Command, Args: args, NativeArgs: e.NativeArgs, Source: source,
		Executor: e.Executor, Target: e.Target, Provenance: e.Provenance,
		ProtocolCwd: protocolCwd, HistoryFiles: e.HistoryFiles, Connections: connections,
		Artifact: e.Artifact, Admission: e.Admission, Limits: e.Limits,
		ProgressFD: e.ProgressFD, StartedAt: e.StartedAt,
	})
}

func (e *Execution) UnmarshalJSON(data []byte) error {
	var wire executionWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*e = Execution{
		SchemaVersion: wire.SchemaVersion, ExecutionID: wire.ExecutionID, FixtureID: wire.FixtureID,
		Command: wire.Command, Args: wire.Args, NativeArgs: wire.NativeArgs, Source: wire.Source,
		Executor: wire.Executor, Target: wire.Target, Provenance: wire.Provenance,
		ProtocolCwd: wire.ProtocolCwd, HistoryFiles: wire.HistoryFiles,
		Connections: wire.Connections, Artifact: wire.Artifact, Admission: wire.Admission,
		Limits: wire.Limits, ProgressFD: wire.ProgressFD, StartedAt: wire.StartedAt,
	}
	return nil
}

// Resolve derives execution layout from the fixture root and execution ID, and
// resolves every persisted relative reference.
func (e *Execution) Resolve(fixture *Fixture, fixtureDir string) error {
	if fixture == nil || fixture.dir != fixtureDir {
		return errors.New("execution resolution requires its fixture")
	}
	if err := validateExecutionID(e.ExecutionID); err != nil {
		return err
	}
	e.dir = fixtureDir
	overlay := ExecutionOverlayRoot(fixtureDir, e.ExecutionID)
	e.Workspace = filepath.Join(overlay, "workspace")
	e.Home = filepath.Join(overlay, "home")
	e.Config = filepath.Join(overlay, "config")
	e.Tmp = filepath.Join(overlay, "tmp")
	e.HistorySeed = filepath.Join(overlay, "history-seed")
	e.ArtifactRoot = filepath.Join(PackageExecutionRoot(fixtureDir, e.Artifact.ID, e.ExecutionID), e.Command)
	if e.Command == "record" {
		e.ArtifactRoot = filepath.Join(PackageExecutionRoot(fixtureDir, e.Artifact.ID, e.ExecutionID), "capture")
	}
	if e.ProtocolCwd == "" {
		e.ProtocolCwd = e.Workspace
	}
	sourcePath, err := ResolveFixtureReference(fixtureDir, e.Source.Path)
	if err != nil {
		return fmt.Errorf("execution source: %w", err)
	}
	e.Source.Path = sourcePath
	if len(e.Args) >= 2 && e.Args[0] == "--fixture-dir" {
		if e.Args[1] != fixtureDir {
			return errors.New("execution arguments reference another fixture root")
		}
	} else {
		e.Args = withFixtureArgs(fixtureDir, e.Args)
	}
	e.Env = baseEnvironment(fixture.AgentType, overlay)
	runtimeEnv, err := executionRuntimeEnvironment(fixture, e.Command, overlay)
	if err != nil {
		return err
	}
	e.RuntimeEnv = runtimeEnv
	switch e.Command {
	case "mock":
		if err := validateExecutionID(e.Artifact.ID); err != nil {
			return fmt.Errorf("execution artifact: %w", err)
		}
		e.Artifact.Path = MaterialRoot(fixtureDir, e.Artifact.ID)
	case "record":
		if err := validateExecutionID(e.Artifact.ID); err != nil {
			return fmt.Errorf("execution artifact: %w", err)
		}
		if len(e.Artifact.Files) > 0 || e.Artifact.ManifestSHA256 != "" {
			e.Artifact.Path = MaterialRoot(fixtureDir, e.Artifact.ID)
		} else {
			e.Artifact.Path = e.ArtifactRoot
		}
	default:
		return fmt.Errorf("unknown execution action %q", e.Command)
	}
	if e.Artifact.FormatVersion == 3 && e.Artifact.OriginSHA256 != "" {
		origin, err := corpus.CaptureOrigin(e.Artifact)
		if err != nil {
			return err
		}
		e.Artifact.OfficialOrigin = origin
	}
	route := fixture.Routes[e.Command]
	for i := range e.Connections {
		connection := &e.Connections[i]
		template, ok := connectionTemplate(route, connection.ID)
		if !ok {
			return fmt.Errorf("connection %s is not declared by its route", connection.ID)
		}
		if connection.Transport != template.Transport {
			return fmt.Errorf("connection %s transport differs from its route", connection.ID)
		}
		connection.Command = template.Command
		connection.Args = slices.Clone(template.Args)
		connection.Launcher = template.Launcher
		receipt, err := ResolvePackageReference(fixtureDir, e.Artifact.ID, connection.Receipt)
		if err != nil {
			return fmt.Errorf("connection %s receipt: %w", connection.ID, err)
		}
		connection.Receipt = receipt
		if connection.ProcessReceipt != "" {
			process, err := ResolvePackageReference(fixtureDir, e.Artifact.ID, connection.ProcessReceipt)
			if err != nil {
				return fmt.Errorf("connection %s process receipt: %w", connection.ID, err)
			}
			connection.ProcessReceipt = process
		}
		connection.Material = e.Artifact.Path
		if connection.MaterialID != "" && connection.MaterialID != e.Artifact.ID {
			return fmt.Errorf("connection %s material identity differs from the execution artifact", connection.ID)
		}
	}
	return nil
}

type IndexConnection struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type IndexExecution struct {
	MaterialID       string            `json:"materialId"`
	Publishing       *Publication      `json:"publishing,omitempty"`
	ID               string            `json:"id"`
	Command          string            `json:"command"`
	DescriptorSHA256 string            `json:"descriptorSHA256"`
	Status           string            `json:"status"`
	ArtifactID       string            `json:"artifactId,omitempty"`
	StartedAt        string            `json:"startedAt"`
	UpdatedAt        string            `json:"updatedAt"`
	Connections      []IndexConnection `json:"connections,omitempty"`
}

type Index struct {
	SchemaVersion  int               `json:"schemaVersion"`
	FixtureID      string            `json:"fixtureId"`
	FixtureSHA256  string            `json:"fixtureSha256"`
	Executions     []IndexExecution  `json:"executions"`
	Artifacts      []corpus.Material `json:"-"`
	Descriptorless map[string]bool   `json:"-"`
	LastUpdatedUTC string            `json:"lastUpdatedUtc"`
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func writeExclusive(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Close())
}

func writeAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func SelfCommand() (Command, string, string, error) {
	self, err := os.Executable()
	if err != nil {
		return Command{}, "", "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return Command{}, "", "", err
	}
	hash, err := tool.FileDigest(self)
	if err != nil {
		return Command{}, "", "", err
	}
	return Command{Command: self}, version.String(), hash, nil
}

func FixturePath(fixtureDir string) string {
	return filepath.Join(fixtureDir, "fixture.json")
}

func IndexPath(fixtureDir string) string {
	return filepath.Join(fixtureDir, "executions", "index.json")
}

func ExecutionDir(fixtureDir, id string) string {
	if validateExecutionID(id) != nil {
		return ""
	}
	var index Index
	if err := tool.ReadJSON(IndexPath(fixtureDir), &index); err != nil || index.SchemaVersion != SchemaVersion {
		return ""
	}
	indexed, err := findIndexExecution(&index, id)
	if err != nil || indexed == nil || validateExecutionID(indexed.MaterialID) != nil {
		return ""
	}
	return PackageExecutionRoot(fixtureDir, indexed.MaterialID, id)
}

func (f Fixture) Validate() error {
	if f.SchemaVersion != SchemaVersion {
		return unsupportedSchema("fixture", f.SchemaVersion)
	}
	if f.FixtureID == "" || f.AgentType == "" {
		return errors.New("incomplete fixture descriptor")
	}
	if err := f.Tool.Validate(); err != nil {
		return fmt.Errorf("fixture tool: %w", err)
	}
	if !filepath.IsAbs(f.ResourceRoot) || !filepath.IsAbs(f.Workspace) || !filepath.IsAbs(f.Home) || !filepath.IsAbs(f.Config) || !filepath.IsAbs(f.Tmp) {
		return errors.New("fixture paths must be absolute")
	}
	if f.dir != "" {
		expected := map[string]string{
			"resourceRoot": ResourceRoot(f.dir),
			"workspace":    BaselineWorkspace(f.dir),
			"home":         filepath.Join(BaselineRoot(f.dir), "home"),
			"config":       filepath.Join(BaselineRoot(f.dir), "config"),
			"tmp":          filepath.Join(BaselineRoot(f.dir), "tmp"),
		}
		actual := map[string]string{
			"resourceRoot": f.ResourceRoot, "workspace": f.Workspace,
			"home": f.Home, "config": f.Config, "tmp": f.Tmp,
		}
		for name, value := range expected {
			if actual[name] != value {
				return fmt.Errorf("fixture %s differs from its fixed layout", name)
			}
		}
	}
	if f.Source.Kind != "case" && f.Source.Kind != "plan" && f.Source.Kind != "recording" {
		return errors.New("invalid fixture source kind")
	}
	if f.Source.Path == "" || !filepath.IsAbs(f.Source.Path) {
		return errors.New("fixture source path must be absolute")
	}
	if f.HistorySeed != "" && !filepath.IsAbs(f.HistorySeed) {
		return errors.New("fixture history seed must be absolute")
	}
	if err := validateFixtureBindings(fixtureSourceSeed, f.HistorySeedFiles); err != nil {
		return fmt.Errorf("fixture history seed: %w", err)
	}
	if f.Source.SHA256 == "" {
		return errors.New("fixture source identity mismatch")
	}
	for _, action := range []string{"record", "mock"} {
		route, ok := f.Routes[action]
		if !ok {
			return fmt.Errorf("fixture omits %s route", action)
		}
		if route.Enabled && (!filepath.IsAbs(route.Workspace) || route.Executor == "" || route.Target == "" || route.Provenance == "") {
			return fmt.Errorf("incomplete %s route", action)
		}
		seen := map[string]bool{}
		for _, connection := range route.Connections {
			if seen[connection.ID] {
				return errors.New("duplicate route connection identity")
			}
			seen[connection.ID] = true
			if err := validateExecutionID(connection.ID); err != nil {
				return err
			}
			if err := f.validateConnectionTemplate(connection); err != nil {
				return fmt.Errorf("%s route connection %s: %w", action, connection.ID, err)
			}
		}
	}
	return nil
}

func validateFixtureBindings(prefix string, bindings map[string]string) error {
	for name, digest := range bindings {
		if err := canonicalReference(name); err != nil {
			return err
		}
		if name != prefix && !strings.HasPrefix(name, prefix+"/") {
			return fmt.Errorf("binding is outside %s: %s", prefix, name)
		}
		if digest == "" || len(digest) != sha256.Size*2 {
			return fmt.Errorf("binding omits a valid SHA-256: %s", name)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return fmt.Errorf("binding omits a valid SHA-256: %s", name)
		}
	}
	return nil
}

func (f Fixture) validateConnectionTemplate(connection ConnectionTemplate) error {
	if connection.ID == "" {
		return errors.New("connection id is empty")
	}
	return f.validateConnectionIdentity(connection.Transport, connection.Command, connection.Args, connection.Launcher)
}

func (f Fixture) validateConnectionIdentity(transport, command string, args []string, launcher string) error {
	switch transport {
	case ConnectionTransportProcess:
		if f.Tool.Source != ToolSourceExecutable {
			return errors.New("embedded fixture declares a process connection")
		}
		if !filepath.IsAbs(command) {
			return errors.New("connection command must be an absolute executable")
		}
		if launcher != "" && !filepath.IsAbs(launcher) {
			return errors.New("connection launcher must be absolute")
		}
		return nil
	case ConnectionTransportInProcess:
		if f.Tool.Source != ToolSourceEmbedded {
			return errors.New("executable fixture declares an in-process connection")
		}
		if command != "" || len(args) != 0 || launcher != "" {
			return errors.New("in-process connection declares executable fields")
		}
		return nil
	default:
		return fmt.Errorf("unsupported connection transport %q", transport)
	}
}

func (e Execution) Validate(f Fixture) error {
	if err := requireSchema("execution", e.SchemaVersion); err != nil {
		return err
	}
	if e.ExecutionID == "" || e.FixtureID != f.FixtureID {
		return errors.New("invalid execution identity")
	}
	if e.Source.Kind != f.Source.Kind || e.Source.ID != f.Source.ID ||
		e.Source.Path != f.Source.Path || e.Source.SHA256 != f.Source.SHA256 ||
		e.Source.Provenance != f.Source.Provenance {
		return errors.New("execution differs from fixture source")
	}
	if e.Command != "record" && e.Command != "mock" {
		return errors.New("execution differs from fixture source or paths")
	}
	if len(e.Args) < 2 || e.Args[0] != "--fixture-dir" || e.dir == "" || e.Args[1] != e.dir {
		return errors.New("execution arguments omit their fixture root")
	}
	if !filepath.IsAbs(e.Workspace) || !filepath.IsAbs(e.ProtocolCwd) ||
		!filepath.IsAbs(e.Home) || !filepath.IsAbs(e.Config) || !filepath.IsAbs(e.Tmp) ||
		!filepath.IsAbs(e.HistorySeed) || !filepath.IsAbs(e.ArtifactRoot) {
		return errors.New("execution paths must be absolute")
	}
	if e.dir != "" {
		overlay := ExecutionOverlayRoot(e.dir, e.ExecutionID)
		expected := map[string]string{
			"workspace":    filepath.Join(overlay, "workspace"),
			"home":         filepath.Join(overlay, "home"),
			"config":       filepath.Join(overlay, "config"),
			"tmp":          filepath.Join(overlay, "tmp"),
			"historySeed":  filepath.Join(overlay, "history-seed"),
			"artifactRoot": filepath.Join(PackageExecutionRoot(e.dir, e.Artifact.ID, e.ExecutionID), e.Command),
		}
		if e.Command == "record" {
			expected["artifactRoot"] = filepath.Join(PackageExecutionRoot(e.dir, e.Artifact.ID, e.ExecutionID), "capture")
		}
		actual := map[string]string{
			"workspace": e.Workspace, "home": e.Home, "config": e.Config,
			"tmp": e.Tmp, "historySeed": e.HistorySeed, "artifactRoot": e.ArtifactRoot,
		}
		for name, value := range expected {
			if actual[name] != value {
				return fmt.Errorf("execution %s differs from its fixed layout", name)
			}
		}
	}
	if len(e.Env) == 0 {
		return errors.New("execution omits its effective environment")
	}
	if e.Executor == "" || e.Target == "" || e.Provenance == "" {
		return errors.New("incomplete execution action")
	}
	if e.Command == "mock" {
		if e.ProgressFD != -1 && e.ProgressFD < 3 {
			return errors.New("invalid mock progress descriptor")
		}
	} else if e.ProgressFD != 0 {
		return errors.New("non-mock execution declares a progress descriptor")
	}
	if err := history.ValidateBindings(e.HistoryFiles); err != nil {
		return err
	}
	if len(e.Connections) == 0 {
		return errors.New("execution omits declared connections")
	}
	if !filepath.IsAbs(e.Artifact.Path) {
		return errors.New("execution artifact path must be absolute")
	}
	seen := map[string]bool{}
	route := f.Routes[e.Command]
	for _, c := range e.Connections {
		if c.ID == "" || !filepath.IsAbs(c.Receipt) || seen[c.ID] {
			return errors.New("invalid or duplicate execution connection")
		}
		template, ok := connectionTemplate(route, c.ID)
		if !ok {
			return fmt.Errorf("connection %s is not declared by its route", c.ID)
		}
		if c.Transport != template.Transport || c.Command != template.Command ||
			!slices.Equal(c.Args, template.Args) || c.Launcher != template.Launcher {
			return fmt.Errorf("connection %s launch contract differs from its route", c.ID)
		}
		if err := f.validateConnectionIdentity(c.Transport, c.Command, c.Args, c.Launcher); err != nil {
			return fmt.Errorf("connection %s: %w", c.ID, err)
		}
		if c.Transport == ConnectionTransportInProcess && c.ProcessReceipt != "" {
			return fmt.Errorf("connection %s in-process connection declares a process receipt", c.ID)
		}
		if c.ProcessReceipt != "" && !filepath.IsAbs(c.ProcessReceipt) {
			return errors.New("connection process receipt must be absolute")
		}
		if c.MaterialID != "" && c.MaterialID != e.Artifact.ID {
			return fmt.Errorf("connection %s material identity differs from the execution artifact", c.ID)
		}
		if e.Command == "mock" && c.MaterialID != e.Artifact.ID {
			return fmt.Errorf("connection %s omits the execution material identity", c.ID)
		}
		if c.Material != e.Artifact.Path {
			return fmt.Errorf("connection %s material differs from the execution artifact", c.ID)
		}
		if e.Command == "record" {
			if c.PlanSHA256 != e.Artifact.PlanSHA256 ||
				c.ManifestSHA256 != e.Artifact.ManifestSHA256 ||
				(e.Artifact.RecordingSHA256 != "" && c.RecordingSHA256 == "") {
				return fmt.Errorf("connection %s material differs from the execution capture", c.ID)
			}
		} else {
			if c.PlanSHA256 != e.Artifact.PlanSHA256 ||
				c.ManifestSHA256 != e.Artifact.ManifestSHA256 ||
				c.RecordingSHA256 == "" {
				return fmt.Errorf("connection %s material differs from the execution artifact", c.ID)
			}
			if c.ID == "main" && c.RecordingSHA256 != e.Artifact.RecordingSHA256 {
				return errors.New("main connection recording differs from the execution artifact")
			}
		}
		seen[c.ID] = true
	}
	return nil
}

func connectionTemplate(route Route, id string) (ConnectionTemplate, bool) {
	for _, connection := range route.Connections {
		if connection.ID == id {
			return connection, true
		}
	}
	return ConnectionTemplate{}, false
}

// persistedSchema reads only the version field so an older or newer control
// plane fails before any schema-specific field is interpreted.
func persistedSchema(path, kind string) error {
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := tool.ReadJSON(path, &header); err != nil {
		return err
	}
	return requireSchema(kind, header.SchemaVersion)
}

func readFixture(fixtureDir string) (*Fixture, error) {
	path := FixturePath(fixtureDir)
	if err := persistedSchema(path, "fixture"); err != nil {
		return nil, err
	}
	var f Fixture
	if err := tool.ReadJSON(path, &f); err != nil {
		return nil, err
	}
	if err := f.Resolve(fixtureDir); err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func readIndex(path string) (*Index, error) {
	if err := persistedSchema(path, "execution index"); err != nil {
		return nil, err
	}
	var index Index
	if err := tool.ReadJSON(path, &index); err != nil {
		return nil, err
	}
	if index.FixtureID == "" || index.FixtureSHA256 == "" {
		return nil, errors.New("invalid execution index")
	}
	fixtureDir, err := CanonicalFixtureDir(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return nil, err
	}
	materialIndexPath := MaterialIndexPath(fixtureDir)
	if err := persistedSchema(materialIndexPath, "material index"); err != nil {
		return nil, err
	}
	var materials MaterialIndex
	if err := tool.ReadJSON(materialIndexPath, &materials); err != nil {
		return nil, err
	}
	if materials.FixtureID != index.FixtureID {
		return nil, errors.New("invalid material index identity")
	}
	seen := map[string]bool{}
	for i := range materials.Materials {
		material := &materials.Materials[i]
		if material.ID == "" || seen[material.ID] {
			return nil, errors.New("duplicate or empty material identity")
		}
		if err := validateExecutionID(material.ID); err != nil {
			return nil, fmt.Errorf("material identity: %w", err)
		}
		seen[material.ID] = true
		material.Path = MaterialRoot(fixtureDir, material.ID)
		if material.FormatVersion == 3 && material.OriginSHA256 != "" {
			origin, err := corpus.CaptureOrigin(*material)
			if err != nil {
				return nil, err
			}
			material.OfficialOrigin = origin
		}
		if _, err := ResolveFixtureReference(fixtureDir, MaterialReference(material.ID)); err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(material.Path)
		if err != nil {
			return nil, err
		}
		if !withinPath(fixtureDir, resolved) {
			return nil, errors.New("material path must remain inside fixture")
		}
	}
	descriptorless := map[string]bool{}
	for _, id := range materials.Descriptorless {
		if id == "" || !seen[id] || descriptorless[id] {
			return nil, errors.New("invalid descriptorless material identity")
		}
		descriptorless[id] = true
	}
	seenExecutions := map[string]bool{}
	for _, execution := range index.Executions {
		if validateExecutionID(execution.ID) != nil || validateExecutionID(execution.MaterialID) != nil || seenExecutions[execution.ID] {
			return nil, errors.New("invalid or duplicate execution/material identity")
		}
		seenExecutions[execution.ID] = true
	}
	index.Artifacts = materials.Materials
	index.Descriptorless = descriptorless
	return &index, nil
}

// ReadExecution reads and resolves one execution descriptor without accepting
// persisted machine-local layout.
func ReadExecution(fixtureDir string, fixture *Fixture, path string) (*Execution, error) {
	if fixture == nil {
		return nil, errors.New("execution resolution requires its fixture")
	}
	dir, err := CanonicalFixtureDir(fixtureDir)
	if err != nil {
		return nil, err
	}
	fixtureDir = dir
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if err := persistedSchema(path, "execution"); err != nil {
		return nil, err
	}
	var execution Execution
	if err := tool.ReadJSON(path, &execution); err != nil {
		return nil, err
	}
	if err := execution.Resolve(fixture, fixtureDir); err != nil {
		return nil, err
	}
	if filepath.Clean(path) != filepath.Join(PackageExecutionRoot(fixtureDir, execution.Artifact.ID, execution.ExecutionID), "execution.json") {
		return nil, errors.New("execution path differs from package identity")
	}
	if err := execution.Validate(*fixture); err != nil {
		return nil, err
	}
	return &execution, nil
}

// MaterialIndex is the immutable-material registry; current.json alone selects
// the input for actions. Execution bookkeeping never selects a material.
type MaterialIndex struct {
	SchemaVersion  int               `json:"schemaVersion"`
	FixtureID      string            `json:"fixtureId"`
	Materials      []corpus.Material `json:"materials"`
	Descriptorless []string          `json:"descriptorless,omitempty"`
}

func MaterialIndexPath(fixtureDir string) string {
	return filepath.Join(fixtureDir, "packages", "index.json")
}

func writeMaterialIndex(fixtureDir string, index *Index) error {
	descriptorless := make([]string, 0, len(index.Descriptorless))
	for _, material := range index.Artifacts {
		if index.Descriptorless[material.ID] {
			descriptorless = append(descriptorless, material.ID)
		}
	}
	slices.Sort(descriptorless)
	return writeAtomic(MaterialIndexPath(fixtureDir), MaterialIndex{
		SchemaVersion: SchemaVersion, FixtureID: index.FixtureID,
		Materials: index.Artifacts, Descriptorless: descriptorless,
	})
}

func unsupportedSchema(kind string, got int) error {
	return fmt.Errorf("unsupported %s schemaVersion %d; schema %d is required", kind, got, SchemaVersion)
}

func requireSchema(kind string, got int) error {
	if got != SchemaVersion {
		return unsupportedSchema(kind, got)
	}
	return nil
}

func withinPath(root, path string) bool {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || (rel != ".." && filepath.IsLocal(rel)))
}

func decodeConnectionObject(data []byte, target any) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, target); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func validateConnectionFields(fields map[string]json.RawMessage, transport string, allowed ...string) error {
	if transport != ConnectionTransportProcess && transport != ConnectionTransportInProcess {
		return nil
	}
	if transport == ConnectionTransportInProcess {
		for _, name := range []string{"command", "args", "argv", "launcher"} {
			if _, present := fields[name]; present {
				return errors.New("in-process connection declares executable fields")
			}
		}
		if _, present := fields["processReceipt"]; present {
			return errors.New("in-process connection declares a process receipt")
		}
	}
	for name := range fields {
		if !slices.Contains(allowed, name) {
			return fmt.Errorf("unsupported connection field %q", name)
		}
	}
	return nil
}
