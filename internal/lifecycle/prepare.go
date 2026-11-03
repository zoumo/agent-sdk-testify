package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validatePreparePaths(sessionDir string, source *sourceSpec) (string, error) {
	if !filepath.IsAbs(sessionDir) {
		return "", errors.New("--session-dir must be an absolute path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(sessionDir))
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, filepath.Base(sessionDir))
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		if err == nil {
			return "", fmt.Errorf("--session-dir already exists: %s", dir)
		}
		return "", err
	}
	if source.source.Kind == "recording" {
		rel, err := filepath.Rel(source.source.Path, dir)
		if err != nil || filepath.IsLocal(rel) {
			return "", errors.New("--session-dir must be outside the recording")
		}
	}
	return dir, nil
}

func tempRoot(sessionDir, sourcePath string) (string, error) {
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", err
	}
	for _, outside := range []string{sessionDir, sourcePath} {
		if outside == "" {
			continue
		}
		rel, err := filepath.Rel(outside, base)
		if err != nil || filepath.IsLocal(rel) {
			return "", errors.New("temporary directory must be outside session and source")
		}
	}
	root, err := os.MkdirTemp(base, "agent-sdk-testify-session-")
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	return root, nil
}

func baseEnvironment(provider, root string) map[string]string {
	env := map[string]string{
		"PATH":            os.Getenv("PATH"),
		"HOME":            filepath.Join(root, "home"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"TMP":             filepath.Join(root, "tmp"),
		"TEMP":            filepath.Join(root, "tmp"),
		"XDG_CONFIG_HOME": filepath.Join(root, "home", ".config"),
		"XDG_CACHE_HOME":  filepath.Join(root, "home", ".cache"),
		"XDG_DATA_HOME":   filepath.Join(root, "home", ".local", "share"),
		"LANG":            "C.UTF-8",
	}
	if provider == "codex" {
		env["CODEX_HOME"] = filepath.Join(root, "config")
	} else {
		env["CLAUDE_CONFIG_DIR"] = filepath.Join(root, "config")
		env["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] = "1"
		env["DISABLE_AUTOUPDATER"] = "1"
	}
	return env
}

func routeEnvironment(session *Session, action string, withAuthentication bool) map[string]string {
	root := filepath.Join(session.ResourceRoot, "routes", action)
	env := baseEnvironment(session.AgentType, root)
	if withAuthentication {
		for key, value := range session.Env {
			if _, special := env[key]; !special {
				env[key] = value
			}
		}
	}
	return env
}

func cleanMockConfig(root string, plan tool.Object, provider string) error {
	config := filepath.Join(root, "config")
	if err := os.RemoveAll(config); err != nil {
		return err
	}
	if err := os.MkdirAll(config, 0o700); err != nil {
		return err
	}
	if provider == "claude" {
		value := map[string]any{"model": planModel(plan)}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(config, "settings.json"), data, 0o600)
	}
	line := ""
	if model := planModel(plan); model != "" {
		line = "model = " + strconv.Quote(model) + "\n"
	}
	return os.WriteFile(filepath.Join(config, "config.toml"), []byte(line), 0o600)
}

func resetRoute(session *Session, action string, plan tool.Object) (string, error) {
	route, ok := session.Routes[action]
	if !ok || !route.Enabled {
		return "", fmt.Errorf("%s is not available for this session", action)
	}
	root := filepath.Join(session.ResourceRoot, "routes", action)
	if err := os.RemoveAll(root); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := copyTree(filepath.Join(session.ResourceRoot, "baseline"), root); err != nil {
		return "", err
	}
	if action == "mock" || action == "verify" {
		if err := cleanMockConfig(root, plan, session.AgentType); err != nil {
			return "", err
		}
	}
	return root, nil
}

func writeConnectionLauncher(root, id, self, sessionDir string) (string, error) {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	invocation := strings.Join([]string{quote(self), quote("mock"), quote("--session-dir"), quote(sessionDir), quote("--connection"), quote(id)}, " ")
	path := filepath.Join(root, "peer-"+id)
	script := "#!/bin/sh\nif [ \"$#\" -eq 1 ] && { [ \"$1\" = '--version' ] || [ \"$1\" = '-v' ]; }; then\n  exec " + invocation + " --version\nfi\nexec " + invocation + "\n"
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(script), 0o700)
}

func connectionIDs(source Source, plan tool.Object) []string {
	if source.Kind == "plan" {
		return []string{"main"}
	}
	if tool.Str(plan["driverProfile"]) == "remote-close" && source.Provenance != "consumer" {
		return []string{"main", "probe"}
	}
	return []string{"main"}
}

func writePlanSnapshot(sessionDir string, raw []byte) (string, error) {
	path := filepath.Join(sessionDir, "source", "plan.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(raw)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return "", err
	}
	return path, nil
}

func initialBuiltin(session *Session, materialRoot string) (*builtin.Material, error) {
	if session.Source.Kind != "case" {
		return nil, nil
	}
	material, _, available, err := builtin.SelectOptional(session.AgentType, session.Source.ID)
	if err != nil || !available {
		return nil, err
	}
	extracted, err := builtin.Extract(material)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(extracted)
	if err := builtin.CheckMaterial(extracted, material); err != nil {
		return nil, err
	}
	if err := copyTree(extracted, materialRoot); err != nil {
		return nil, err
	}
	return material, nil
}

// Prepare creates a persistent session directory and its disposable owned
// baseline. It never starts a client, SDK, driver or native Agent.
func Prepare(o PrepareOptions) (result *Session, err error) {
	spec, err := resolveSource(o)
	if err != nil {
		return nil, err
	}
	sessionDir, err := validatePreparePaths(o.SessionDir, spec)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(sessionDir, 0o700); err != nil {
		return nil, err
	}
	var root string
	defer func() {
		if err != nil {
			diagnostic := map[string]any{"schemaVersion": SchemaVersion, "status": "failed", "error": err.Error(), "resourceRoot": root}
			if root != "" {
				cleanup := os.RemoveAll(root)
				diagnostic["cleanupComplete"] = cleanup == nil
				if cleanup != nil {
					diagnostic["cleanupError"] = cleanup.Error()
				}
			}
			_ = writeAtomic(filepath.Join(sessionDir, "prepare.json"), diagnostic)
		}
	}()
	root, err = tempRoot(sessionDir, spec.source.Path)
	if err != nil {
		return nil, err
	}
	baseline := filepath.Join(root, "baseline")
	for _, name := range []string{"workspace", "home", "config", "tmp", "history-seed"} {
		if err := os.MkdirAll(filepath.Join(baseline, name), 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".owner"), nil, 0o600); err != nil {
		return nil, err
	}
	sessionID, err := randomID()
	if err != nil {
		return nil, err
	}
	self, version, toolHash, err := SelfCommand()
	if err != nil {
		return nil, err
	}
	agentType := spec.agentType
	if err := validateAgent(agentType); err != nil {
		return nil, err
	}
	baselineEnv := baseEnvironment(agentType, baseline)
	if err := official.CopyNativeConfig(agentType, filepath.Join(baseline, "config"), baselineEnv); err != nil {
		return nil, err
	}
	files, err := planFiles(spec.plan)
	if err != nil {
		return nil, err
	}
	if err := tool.WriteFiles(filepath.Join(baseline, "workspace"), files); err != nil {
		return nil, err
	}
	historySeed := ""
	if o.HistorySeed != "" {
		if !filepath.IsAbs(o.HistorySeed) {
			return nil, errors.New("--history-seed must be absolute")
		}
		if spec.source.Kind == "recording" {
			return nil, errors.New("history seed requires a case or plan source")
		}
		sourceSeed, err := filepath.EvalSymlinks(o.HistorySeed)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(sourceSeed)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("--history-seed must be an unlinked directory")
		}
		rel, err := filepath.Rel(sourceSeed, sessionDir)
		if err != nil || filepath.IsLocal(rel) {
			return nil, errors.New("--session-dir must be outside the history seed")
		}
		historySeed = filepath.Join(sessionDir, "source", "history-seed")
		if err := copyTree(sourceSeed, historySeed); err != nil {
			return nil, err
		}
		if spec.source.Kind == "plan" {
			err = copyTree(historySeed, filepath.Join(baseline, "history-seed"))
		}
		if err != nil {
			return nil, err
		}
	}
	session := &Session{
		SchemaVersion: SchemaVersion,
		SessionID:     sessionID,
		Tool:          self,
		ToolVersion:   version,
		ToolSHA256:    toolHash,
		AgentType:     agentType,
		Source:        spec.source,
		SourceSHA256:  spec.source.SHA256,
		HistorySeed:   historySeed,
		ResourceRoot:  root,
		Workspace:     filepath.Join(baseline, "workspace"),
		Home:          filepath.Join(baseline, "home"),
		Config:        filepath.Join(baseline, "config"),
		Tmp:           filepath.Join(baseline, "tmp"),
		ProtocolCwd:   planProtocolCwd(spec.plan, filepath.Join(baseline, "workspace")),
		Env:           baselineEnv,
		Routes:        map[string]Route{},
		Finish:        Command{Command: self.Command, Args: []string{"finish", "--session-dir", sessionDir}},
		Limits: Limits{
			ProtocolBytes: o.ProtocolBytes,
			StderrBytes:   o.StderrBytes,
			MaxFrames:     o.MaxFrames,
			MaxFrameBytes: o.MaxFrameBytes,
		},
	}
	if err := os.WriteFile(filepath.Join(root, ".owner"), []byte(sessionID), 0o600); err != nil {
		return nil, err
	}
	if spec.source.Kind == "case" || spec.source.Kind == "plan" {
		path, err := writePlanSnapshot(sessionDir, spec.planBytes)
		if err != nil {
			return nil, err
		}
		session.Source.Path = path
	}
	ids := connectionIDs(session.Source, spec.plan)
	for _, action := range []string{"record", "verify", "mock"} {
		if !spec.routes[action] {
			session.Routes[action] = Route{Enabled: false}
			continue
		}
		routeRoot := filepath.Join(root, "routes", action)
		if err := os.MkdirAll(routeRoot, 0o700); err != nil {
			return nil, err
		}
		if err := copyTree(baseline, routeRoot); err != nil {
			return nil, err
		}
		if action == "mock" || action == "verify" {
			if err := cleanMockConfig(routeRoot, spec.plan, agentType); err != nil {
				return nil, err
			}
		}
		route := Route{
			Enabled:     true,
			Executor:    map[string]string{"record": "official-driver", "verify": "official-driver", "mock": "external-sdk"}[action],
			Target:      map[string]string{"record": "native-agent", "verify": "replay", "mock": "replay"}[action],
			Provenance:  spec.provenance,
			Workspace:   filepath.Join(routeRoot, "workspace"),
			ProtocolCwd: planProtocolCwd(spec.plan, filepath.Join(routeRoot, "workspace")),
			Env:         routeEnvironment(session, action, action == "record"),
		}
		if action == "record" && spec.source.Kind == "plan" {
			route.Executor = "external-sdk"
			route.Provenance = "consumer"
		}
		for _, id := range ids {
			template := ConnectionTemplate{ID: id, Command: self.Command, Args: []string{"mock", "--session-dir", sessionDir, "--connection", id}}
			switch action {
			case "record":
				template.Args = []string{"record", "--session-dir", sessionDir}
			case "verify":
				template.Args = []string{"verify", "--session-dir", sessionDir}
			}
			if action == "mock" {
				template.Launcher, err = writeConnectionLauncher(filepath.Join(root, "launchers"), id, self.Command, sessionDir)
				if err != nil {
					return nil, err
				}
			}
			route.Connections = append(route.Connections, template)
		}
		session.Routes[action] = route
	}
	materialRoot := filepath.Join(root, "initial")
	var materialArtifact *Artifact
	switch session.Source.Kind {
	case "case":
		material, err := initialBuiltin(session, materialRoot)
		if err != nil {
			return nil, err
		}
		if material != nil {
			artifact, err := artifactForMaterial(materialRoot, "builtin", "initial", ids)
			if err != nil {
				return nil, err
			}
			materialArtifact = artifact
		}
	case "recording":
		artifact, err := artifactForMaterial(session.Source.Path, session.Source.Provenance, "initial", ids)
		if err != nil {
			return nil, err
		}
		materialArtifact = artifact
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	sessionPath := SessionPath(sessionDir)
	if err := writeExclusive(sessionPath, session); err != nil {
		return nil, err
	}
	sessionHash, err := digestFile(sessionPath)
	if err != nil {
		return nil, err
	}
	index := &Index{SchemaVersion: SchemaVersion, SessionID: session.SessionID, SessionSHA256: sessionHash, LastUpdatedUTC: now()}
	if materialArtifact != nil {
		materialArtifact.Action = "mock"
		materialArtifact.CreatedAt = now()
		index.Artifacts = append(index.Artifacts, *materialArtifact)
	}
	if err := writeExclusive(IndexPath(sessionDir), index); err != nil {
		return nil, err
	}
	return session, nil
}
