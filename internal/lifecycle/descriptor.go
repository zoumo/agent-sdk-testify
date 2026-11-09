// Package lifecycle owns fixture identity, environment routes,
// execution descriptors and finalization. Protocol replay and native process
// behavior stay in their existing packages.
package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
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

const SchemaVersion = 5

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

type ToolIdentity struct {
	Source         ToolSource `json:"source"`
	Version        string     `json:"version"`
	Command        string     `json:"command,omitempty"`
	Args           []string   `json:"args,omitempty"`
	SHA256         string     `json:"sha256,omitempty"`
	Module         string     `json:"module,omitempty"`
	ModuleSum      string     `json:"moduleSum,omitempty"`
	ModuleRevision string     `json:"moduleRevision,omitempty"`
	ModuleTime     string     `json:"moduleTime,omitempty"`
	GoVersion      string     `json:"goVersion,omitempty"`
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
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Launcher  string   `json:"launcher,omitempty"`
}

func (c *ConnectionTemplate) UnmarshalJSON(data []byte) error {
	type connectionTemplate ConnectionTemplate
	var decoded connectionTemplate
	fields, err := decodeConnectionObject(data, &decoded)
	if err != nil {
		return err
	}
	if err := validateConnectionFields(fields, decoded.Transport, "id", "transport", "command", "args", "launcher"); err != nil {
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
	Workspace   string               `json:"workspace"`
	ProtocolCwd string               `json:"protocolCwd"`
	Env         map[string]string    `json:"env"`
	RuntimeEnv  []string             `json:"runtimeEnv,omitempty"`
	Connections []ConnectionTemplate `json:"connections"`
}

type Fixture struct {
	SchemaVersion    int               `json:"schemaVersion"`
	FixtureID        string            `json:"fixtureId"`
	Tool             ToolIdentity      `json:"tool"`
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
	Transport       string   `json:"transport"`
	Command         string   `json:"command,omitempty"`
	Args            []string `json:"args,omitempty"`
	Launcher        string   `json:"launcher,omitempty"`
	Material        string   `json:"material,omitempty"`
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
		"id", "transport", "command", "args", "launcher", "material",
		"planSHA256", "manifestSHA256", "recordingSHA256", "receipt", "processReceipt",
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
	return Command{Command: self}, version.String(), hash, nil
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
	if err := requireSchema("execution index", index.SchemaVersion); err != nil {
		return nil, err
	}
	if index.FixtureID == "" || index.FixtureSHA256 == "" {
		return nil, errors.New("invalid execution index")
	}
	var materials MaterialIndex
	if err := tool.ReadJSON(MaterialIndexPath(filepath.Dir(filepath.Dir(path))), &materials); err != nil {
		return nil, err
	}
	if err := requireSchema("material index", materials.SchemaVersion); err != nil {
		return nil, err
	}
	if materials.FixtureID != index.FixtureID {
		return nil, errors.New("invalid material index identity")
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
