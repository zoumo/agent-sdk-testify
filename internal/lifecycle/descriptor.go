// Package lifecycle owns fixture identity, environment routes,
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

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

const SchemaVersion = 4

const (
	ToolSourceExecutable = "executable"
	ToolSourceEmbedded   = "embedded"
)

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
	Embedded bool     `json:"embedded,omitempty"`
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

type Fixture struct {
	SchemaVersion    int               `json:"schemaVersion"`
	FixtureID        string            `json:"fixtureId"`
	Tool             Command           `json:"tool"`
	ToolVersion      string            `json:"toolVersion"`
	ToolSHA256       string            `json:"toolSHA256"`
	ToolSource       string            `json:"toolSource,omitempty"`
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
	Embedded        bool     `json:"embedded,omitempty"`
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
	Artifact      corpus.Material       `json:"artifact"`
	Admission     *Admission            `json:"admission,omitempty"`
	Limits        Limits                `json:"limits"`
	ProgressFD    int                   `json:"progressFd,omitempty"`
	StartedAt     string                `json:"startedAt"`
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
	SchemaVersion  int               `json:"schemaVersion"`
	FixtureID      string            `json:"fixtureId"`
	FixtureSHA256  string            `json:"fixtureSha256"`
	Executions     []IndexExecution  `json:"executions"`
	Artifacts      []corpus.Material `json:"-"`
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
	return Command{Command: self}, version.Version, hash, nil
}

func FixturePath(fixtureDir string) string {
	return filepath.Join(fixtureDir, "fixture.json")
}

func IndexPath(fixtureDir string) string {
	return filepath.Join(fixtureDir, "executions", "index.json")
}

func ExecutionDir(fixtureDir, id string) string {
	return filepath.Join(fixtureDir, "executions", id)
}

func (f Fixture) Validate() error {
	if f.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported fixture schemaVersion %d", f.SchemaVersion)
	}
	if f.FixtureID == "" || f.AgentType == "" || f.ToolVersion == "" || f.Tool.Command == "" {
		return errors.New("incomplete fixture descriptor")
	}
	switch f.ToolSource {
	case "", ToolSourceExecutable, ToolSourceEmbedded:
	default:
		return fmt.Errorf("unsupported fixture toolSource %q", f.ToolSource)
	}
	if f.ToolSource == ToolSourceEmbedded {
		if f.Tool.Command != version.Module || f.ToolSHA256 != "" {
			return errors.New("embedded fixture has an invalid tool identity")
		}
	}
	if !filepath.IsAbs(f.ResourceRoot) || !filepath.IsAbs(f.Workspace) || !filepath.IsAbs(f.Home) || !filepath.IsAbs(f.Config) || !filepath.IsAbs(f.Tmp) {
		return errors.New("fixture paths must be absolute")
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
	if err := history.ValidateBindings(f.HistorySeedFiles); err != nil {
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
		for _, connection := range route.Connections {
			if err := f.validateConnectionTemplate(connection); err != nil {
				return fmt.Errorf("%s route connection %s: %w", action, connection.ID, err)
			}
		}
	}
	return nil
}

func (f Fixture) validateConnectionTemplate(connection ConnectionTemplate) error {
	if connection.ID == "" {
		return errors.New("connection id is empty")
	}
	return f.validateConnectionIdentity(connection.Embedded, connection.Command, connection.Launcher)
}

func (f Fixture) validateConnectionIdentity(embedded bool, command, launcher string) error {
	if embedded {
		if f.ToolSource != ToolSourceEmbedded {
			return errors.New("executable fixture declares an embedded connection")
		}
		if command != version.Module || command != f.Tool.Command {
			return errors.New("embedded connection command differs from the library identity")
		}
		if launcher != "" {
			return errors.New("embedded connection declares an executable launcher")
		}
		return nil
	}
	if f.ToolSource == ToolSourceEmbedded {
		return errors.New("embedded fixture declares an executable connection")
	}
	if !filepath.IsAbs(command) {
		return errors.New("connection command must be an absolute executable")
	}
	if launcher != "" && !filepath.IsAbs(launcher) {
		return errors.New("connection launcher must be absolute")
	}
	return nil
}

func (e Execution) Validate(f Fixture) error {
	if e.SchemaVersion != SchemaVersion || e.ExecutionID == "" || e.FixtureID != f.FixtureID {
		return errors.New("invalid execution identity or schemaVersion")
	}
	if e.Source != f.Source || (e.Command != "record" && e.Command != "mock") {
		return errors.New("execution differs from fixture source or paths")
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
	route := f.Routes[e.Command]
	for _, c := range e.Connections {
		if c.ID == "" || !filepath.IsAbs(c.Receipt) || seen[c.ID] {
			return errors.New("invalid or duplicate execution connection")
		}
		template, ok := connectionTemplate(route, c.ID)
		if !ok {
			return fmt.Errorf("connection %s is not declared by its route", c.ID)
		}
		if c.Embedded != template.Embedded || c.Command != template.Command ||
			!slices.Equal(c.Args, template.Args) || c.Launcher != template.Launcher {
			return fmt.Errorf("connection %s launch contract differs from its route", c.ID)
		}
		if err := f.validateConnectionIdentity(c.Embedded, c.Command, c.Launcher); err != nil {
			return fmt.Errorf("connection %s: %w", c.ID, err)
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

func readFixture(path string) (*Fixture, error) {
	var f Fixture
	if err := tool.ReadJSON(path, &f); err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func readIndex(path string) (*Index, error) {
	var index Index
	if err := tool.ReadJSON(path, &index); err != nil {
		return nil, err
	}
	if index.SchemaVersion != SchemaVersion || index.FixtureID == "" || index.FixtureSHA256 == "" {
		return nil, errors.New("invalid execution index")
	}
	var materials MaterialIndex
	if err := tool.ReadJSON(MaterialIndexPath(filepath.Dir(filepath.Dir(path))), &materials); err != nil {
		return nil, err
	}
	if materials.SchemaVersion != SchemaVersion || materials.FixtureID != index.FixtureID {
		return nil, errors.New("invalid material index identity or schemaVersion")
	}
	fixtureDir := filepath.Dir(filepath.Dir(path))
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
		if resolved != material.Path || !withinPath(fixtureDir, resolved) {
			return nil, errors.New("material path must remain inside fixture")
		}
	}
	index.Artifacts = materials.Materials
	return &index, nil
}

// MaterialIndex is the immutable-material registry; current.json alone selects
// the input for actions. Execution bookkeeping never selects a material.
type MaterialIndex struct {
	SchemaVersion int               `json:"schemaVersion"`
	FixtureID     string            `json:"fixtureId"`
	Materials     []corpus.Material `json:"materials"`
}

func MaterialIndexPath(fixtureDir string) string {
	return filepath.Join(fixtureDir, "materials", "index.json")
}

func writeMaterialIndex(fixtureDir string, index *Index) error {
	return writeAtomic(MaterialIndexPath(fixtureDir), MaterialIndex{SchemaVersion: SchemaVersion, FixtureID: index.FixtureID, Materials: index.Artifacts})
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
