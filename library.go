package agenttestify

import (
	"maps"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/application"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
)

// ToolSource identifies how the library or CLI entered the prepared session.
type ToolSource string

const (
	ToolSourceExecutable ToolSource = lifecycle.ToolSourceExecutable
	ToolSourceEmbedded   ToolSource = lifecycle.ToolSourceEmbedded
)

// PrepareOptions selects the immutable initial state for a new session.
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
}

// ExportOptions names the session and a new directory outside it.
type ExportOptions struct {
	SessionDir string
	Output     string
}

// ToolIdentity describes the code that prepared a session.
type ToolIdentity struct {
	Command string
	Args    []string
	Version string
	SHA256  string
	Source  ToolSource
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
	ID       string
	Command  string
	Args     []string
	Launcher string
	Embedded bool
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

// Session is a detached, typed view of a prepared session.
type Session struct {
	SessionDir   string
	SessionID    string
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

// Prepare creates one session using the embedded library tool identity.
func Prepare(options PrepareOptions) (*Session, error) {
	_, err := application.Prepare(application.PrepareOptions{
		SessionDir:    options.SessionDir,
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
	result, err := application.Open(options.SessionDir)
	if err != nil {
		return nil, err
	}
	return sessionView(result), nil
}

// Open reads a prepared session without changing it.
func Open(sessionDir string) (*Session, error) {
	result, err := application.Open(sessionDir)
	if err != nil {
		return nil, err
	}
	return sessionView(result), nil
}

// Export copies the current material package without changing the session.
func Export(options ExportOptions) error {
	return application.Export(application.ExportOptions{
		SessionDir: options.SessionDir,
		Output:     options.Output,
	})
}

func sessionView(result *application.OpenResult) *Session {
	session := result.Session
	view := &Session{
		SessionDir:   result.Dir,
		SessionID:    session.SessionID,
		AgentType:    session.AgentType,
		HistorySeed:  session.HistorySeed,
		ResourceRoot: session.ResourceRoot,
		Workspace:    session.Workspace,
		Home:         session.Home,
		Config:       session.Config,
		Tmp:          session.Tmp,
		ProtocolCwd:  session.ProtocolCwd,
		Env:          maps.Clone(session.Env),
		Limits:       limitsView(session.Limits),
		Tool: ToolIdentity{
			Command: session.Tool.Command,
			Args:    slices.Clone(session.Tool.Args),
			Version: session.ToolVersion,
			SHA256:  session.ToolSHA256,
			Source:  ToolSourceExecutable,
		},
		Source: Source{
			Kind:          session.Source.Kind,
			ID:            session.Source.ID,
			Path:          session.Source.Path,
			SHA256:        session.Source.SHA256,
			Provenance:    session.Source.Provenance,
			DriverProfile: session.Source.DriverProfile,
		},
	}
	if session.ToolSource == lifecycle.ToolSourceEmbedded {
		view.Tool.Source = ToolSourceEmbedded
	}
	view.Routes = make(map[string]Route, len(session.Routes))
	for name, route := range session.Routes {
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
			ID:       connection.ID,
			Command:  connection.Command,
			Args:     slices.Clone(connection.Args),
			Launcher: connection.Launcher,
			Embedded: connection.Embedded,
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
