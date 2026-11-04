// Package lifecycle owns prepared-session identity, environment routes,
// execution descriptors and finalization. Protocol replay and native process
// behavior stay in their existing packages.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

const SchemaVersion = 2

type Command struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
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
	ID       string   `json:"id"`
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
	Launcher string   `json:"launcher,omitempty"`
}

type Route struct {
	Enabled     bool                 `json:"enabled"`
	Executor    string               `json:"executor"`
	Target      string               `json:"target"`
	Provenance  string               `json:"provenance"`
	Workspace   string               `json:"workspace"`
	ProtocolCwd string               `json:"protocolCwd"`
	Env         map[string]string    `json:"env"`
	RuntimeEnv  []string             `json:"runtimeEnv,omitempty"`
	Connections []ConnectionTemplate `json:"connections"`
}

type Session struct {
	SchemaVersion    int               `json:"schemaVersion"`
	SessionID        string            `json:"sessionId"`
	Tool             Command           `json:"tool"`
	ToolVersion      string            `json:"toolVersion"`
	ToolSHA256       string            `json:"toolSHA256"`
	AgentType        string            `json:"agentType"`
	Source           Source            `json:"source"`
	HistorySeed      string            `json:"historySeed,omitempty"`
	HistorySeedFiles map[string]string `json:"historySeedFiles,omitempty"`
	ResourceRoot     string            `json:"resourceRoot"`
	Workspace        string            `json:"workspace"`
	Home             string            `json:"home"`
	Config           string            `json:"config"`
	Tmp              string            `json:"tmp"`
	ProtocolCwd      string            `json:"protocolCwd,omitempty"`
	Env              map[string]string `json:"env"`
	Routes           map[string]Route  `json:"routes"`
	Finish           Command           `json:"finish"`
	Limits           Limits            `json:"limits"`
}

type ExecutionConnection struct {
	ID              string   `json:"id"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	Launcher        string   `json:"launcher,omitempty"`
	Material        string   `json:"material,omitempty"`
	PlanSHA256      string   `json:"planSHA256,omitempty"`
	ManifestSHA256  string   `json:"manifestSHA256,omitempty"`
	RecordingSHA256 string   `json:"recordingSHA256,omitempty"`
	Receipt         string   `json:"receipt"`
	ProcessReceipt  string   `json:"processReceipt,omitempty"`
}

type Admission struct {
	Index     string `json:"index"`
	Scenarios string `json:"scenarios"`
	Archive   string `json:"archive"`
}

type Execution struct {
	SchemaVersion int                   `json:"schemaVersion"`
	ExecutionID   string                `json:"executionId"`
	SessionID     string                `json:"sessionId"`
	Command       string                `json:"command"`
	Args          []string              `json:"args,omitempty"`
	NativeArgs    []string              `json:"nativeArgs,omitempty"`
	Source        Source                `json:"source"`
	Executor      string                `json:"executor"`
	Target        string                `json:"target"`
	Provenance    string                `json:"provenance"`
	Workspace     string                `json:"workspace"`
	ProtocolCwd   string                `json:"protocolCwd"`
	Home          string                `json:"home"`
	Config        string                `json:"config"`
	Tmp           string                `json:"tmp"`
	HistorySeed   string                `json:"historySeed"`
	HistoryFiles  map[string]string     `json:"historyFiles,omitempty"`
	Env           map[string]string     `json:"env"`
	RuntimeEnv    map[string]string     `json:"-"`
	Connections   []ExecutionConnection `json:"connections"`
	ArtifactRoot  string                `json:"artifactRoot"`
	Artifact      Artifact              `json:"artifact"`
	Admission     *Admission            `json:"admission,omitempty"`
	Limits        Limits                `json:"limits"`
	ProgressFD    int                   `json:"progressFd,omitempty"`
	StartedAt     string                `json:"startedAt"`
}

type Artifact struct {
	OfficialOrigin  *OfficialOrigin   `json:"officialOrigin,omitempty"`
	Files           map[string]string `json:"files,omitempty"`
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Action          string            `json:"action"`
	Provenance      string            `json:"provenance"`
	Path            string            `json:"path"`
	PlanSHA256      string            `json:"planSHA256"`
	ManifestSHA256  string            `json:"manifestSHA256"`
	RecordingSHA256 string            `json:"recordingSHA256"`
	Connections     []string          `json:"connections"`
	CreatedAt       string            `json:"createdAt"`
}

type IndexConnection struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type IndexExecution struct {
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
	SchemaVersion  int              `json:"schemaVersion"`
	SessionID      string           `json:"sessionId"`
	SessionSHA256  string           `json:"sessionSHA256"`
	Executions     []IndexExecution `json:"executions"`
	Artifacts      []Artifact       `json:"-"`
	LastUpdatedUTC string           `json:"lastUpdatedUtc"`
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
	return Command{Command: self}, version.Version, hash, nil
}

func SessionPath(sessionDir string) string {
	return filepath.Join(sessionDir, "session.json")
}

func IndexPath(sessionDir string) string {
	return filepath.Join(sessionDir, "executions", "index.json")
}

func ExecutionDir(sessionDir, id string) string {
	return filepath.Join(sessionDir, "executions", id)
}

func (s Session) Validate() error {
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported session schemaVersion %d", s.SchemaVersion)
	}
	if s.SessionID == "" || s.AgentType == "" || s.ToolVersion == "" || s.Tool.Command == "" {
		return errors.New("incomplete session descriptor")
	}
	if !filepath.IsAbs(s.ResourceRoot) || !filepath.IsAbs(s.Workspace) || !filepath.IsAbs(s.Home) || !filepath.IsAbs(s.Config) || !filepath.IsAbs(s.Tmp) {
		return errors.New("session paths must be absolute")
	}
	if s.Source.Kind != "case" && s.Source.Kind != "plan" && s.Source.Kind != "recording" {
		return errors.New("invalid session source kind")
	}
	if s.Source.Path == "" || !filepath.IsAbs(s.Source.Path) {
		return errors.New("session source path must be absolute")
	}
	if s.HistorySeed != "" && !filepath.IsAbs(s.HistorySeed) {
		return errors.New("session history seed must be absolute")
	}
	if err := history.ValidateBindings(s.HistorySeedFiles); err != nil {
		return fmt.Errorf("session history seed: %w", err)
	}
	if s.Source.SHA256 == "" {
		return errors.New("session source identity mismatch")
	}
	for _, action := range []string{"record", "mock"} {
		route, ok := s.Routes[action]
		if !ok {
			return fmt.Errorf("session omits %s route", action)
		}
		if route.Enabled && (!filepath.IsAbs(route.Workspace) || route.Executor == "" || route.Target == "" || route.Provenance == "") {
			return fmt.Errorf("incomplete %s route", action)
		}
	}
	return nil
}

func (e Execution) Validate(s Session) error {
	if e.SchemaVersion != SchemaVersion || e.ExecutionID == "" || e.SessionID != s.SessionID {
		return errors.New("invalid execution identity or schemaVersion")
	}
	if e.Source != s.Source || (e.Command != "record" && e.Command != "mock") {
		return errors.New("execution differs from session source or paths")
	}
	if !filepath.IsAbs(e.Workspace) || !filepath.IsAbs(e.ProtocolCwd) ||
		!filepath.IsAbs(e.Home) || !filepath.IsAbs(e.Config) || !filepath.IsAbs(e.Tmp) ||
		!filepath.IsAbs(e.HistorySeed) || !filepath.IsAbs(e.ArtifactRoot) {
		return errors.New("execution paths must be absolute")
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
	seen := map[string]bool{}
	route := s.Routes[e.Command]
	for _, c := range e.Connections {
		if c.ID == "" || !filepath.IsAbs(c.Receipt) || seen[c.ID] {
			return errors.New("invalid or duplicate execution connection")
		}
		template, ok := connectionTemplate(route, c.ID)
		if !ok {
			return fmt.Errorf("connection %s is not declared by its route", c.ID)
		}
		if !filepath.IsAbs(c.Command) || c.Command != template.Command ||
			!slices.Equal(c.Args, template.Args) || c.Launcher != template.Launcher {
			return fmt.Errorf("connection %s launch contract differs from its route", c.ID)
		}
		if c.Launcher != "" && !filepath.IsAbs(c.Launcher) {
			return fmt.Errorf("connection %s launcher must be absolute", c.ID)
		}
		if c.Material != "" && !filepath.IsAbs(c.Material) {
			return errors.New("connection material must be absolute")
		}
		if c.ProcessReceipt != "" && !filepath.IsAbs(c.ProcessReceipt) {
			return errors.New("connection process receipt must be absolute")
		}
		if e.Command == "record" {
			if c.Material != e.Artifact.Path ||
				c.PlanSHA256 != e.Artifact.PlanSHA256 ||
				c.ManifestSHA256 != e.Artifact.ManifestSHA256 ||
				(e.Artifact.RecordingSHA256 != "" && c.RecordingSHA256 == "") {
				return fmt.Errorf("connection %s material differs from the execution capture", c.ID)
			}
		} else {
			if c.Material != e.Artifact.Path ||
				c.PlanSHA256 != e.Artifact.PlanSHA256 ||
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

func readSession(path string) (*Session, error) {
	var s Session
	if err := tool.ReadJSON(path, &s); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func readIndex(path string) (*Index, error) {
	var index Index
	if err := tool.ReadJSON(path, &index); err != nil {
		return nil, err
	}
	if index.SchemaVersion != SchemaVersion || index.SessionID == "" || index.SessionSHA256 == "" {
		return nil, errors.New("invalid execution index")
	}
	var materials MaterialIndex
	if err := tool.ReadJSON(MaterialIndexPath(filepath.Dir(filepath.Dir(path))), &materials); err != nil {
		return nil, err
	}
	if materials.SchemaVersion != SchemaVersion || materials.SessionID != index.SessionID {
		return nil, errors.New("invalid material index identity or schemaVersion")
	}
	sessionDir := filepath.Dir(filepath.Dir(path))
	seen := map[string]bool{}
	for _, material := range materials.Materials {
		if material.ID == "" || seen[material.ID] {
			return nil, errors.New("duplicate or empty material identity")
		}
		seen[material.ID] = true
		resolved, err := filepath.EvalSymlinks(material.Path)
		if err != nil {
			return nil, err
		}
		if resolved != material.Path || !withinPath(sessionDir, resolved) {
			return nil, errors.New("material path must remain inside session")
		}
	}
	index.Artifacts = materials.Materials
	return &index, nil
}

// MaterialIndex is the immutable-material registry; current.json alone selects
// the input for actions. Execution bookkeeping never selects a material.
type MaterialIndex struct {
	SchemaVersion int        `json:"schemaVersion"`
	SessionID     string     `json:"sessionId"`
	Materials     []Artifact `json:"materials"`
}

func MaterialIndexPath(sessionDir string) string {
	return filepath.Join(sessionDir, "materials", "index.json")
}

func writeMaterialIndex(sessionDir string, index *Index) error {
	return writeAtomic(MaterialIndexPath(sessionDir), MaterialIndex{SchemaVersion: SchemaVersion, SessionID: index.SessionID, Materials: index.Artifacts})
}
