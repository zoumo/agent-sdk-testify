package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/native"
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
	if profile, ok := agentProfiles[provider]; ok {
		maps.Copy(env, profile.environment(root))
	}
	return env
}

var routeEnvironmentEntries = []string{"workspace", "home", "config", "tmp", "history-seed"}

func bindRouteSlots(routeRoot, targetRoot string) error {
	info, err := os.Lstat(targetRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("route target is not an unlinked directory: %s", targetRoot)
	}
	if err := os.MkdirAll(routeRoot, 0o700); err != nil {
		return err
	}
	for _, name := range routeEnvironmentEntries {
		target := filepath.Join(targetRoot, name)
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("route entry is not an unlinked directory: %s", target)
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		temp := filepath.Join(routeRoot, "."+name+"-"+id)
		if err := os.Symlink(target, temp); err != nil {
			return err
		}
		if err := os.Rename(temp, filepath.Join(routeRoot, name)); err != nil {
			_ = os.Remove(temp)
			return err
		}
	}
	return nil
}

func restoreRouteSlots(session *Session, action string) error {
	return bindRouteSlots(
		filepath.Join(session.ResourceRoot, "routes", action),
		filepath.Join(session.ResourceRoot, "templates", action),
	)
}

func cleanMockConfig(root string, plan tool.Object, provider string) error {
	profile, err := agentProfileFor(provider)
	if err != nil {
		return err
	}
	return profile.writeConfig(root, plan)
}

func writeConnectionLauncher(root, id, self, sessionDir string) (string, error) {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	invocation := strings.Join([]string{quote(self), quote("agent"), quote("mock"), quote("--session-dir"), quote(sessionDir), quote("--connection"), quote(id)}, " ")
	path := filepath.Join(root, "peer-"+id)
	script := "#!/bin/sh\nexec " + invocation + " -- \"$@\"\n"
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(script), 0o700)
}

func ConnectionIDs(source Source, plan tool.Object) []string {
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
	if err := copyMaterial(extracted, materialRoot); err != nil {
		return nil, err
	}
	return material, nil
}

// Prepare creates a caller-owned session directory containing its runtime
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
			_ = os.Remove(SessionPath(sessionDir))
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
	root = filepath.Join(sessionDir, "runtime")
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	baseline := filepath.Join(root, "baseline")
	for _, name := range []string{"workspace", "home", "config", "tmp", "history-seed"} {
		if err := os.MkdirAll(filepath.Join(baseline, name), 0o700); err != nil {
			return nil, err
		}
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
	privateEnv := map[string]string{}
	if err := native.CopyNativeConfig(agentType, filepath.Join(baseline, "config"), filepath.Join(root, "private-config"), privateEnv); err != nil {
		return nil, err
	}
	files, err := catalog.PlanFiles(spec.plan)
	if err != nil {
		return nil, err
	}
	if err := tool.WriteFiles(filepath.Join(baseline, "workspace"), files); err != nil {
		return nil, err
	}
	historySeed := ""
	var historySeedFiles map[string]string
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
		if err := copyMaterial(sourceSeed, historySeed); err != nil {
			return nil, err
		}
		files, err := history.HashTree(historySeed)
		if err != nil {
			return nil, err
		}
		historySeedFiles = make(map[string]string, len(files))
		for name, digest := range files {
			historySeedFiles["history-seed/"+name] = digest
		}
		if spec.source.Kind == "case" && spec.plan["driverProfile"] == "existing-children" {
			if err := native.PrepareHistorySeed(historySeed, baseline, spec.plan); err != nil {
				return nil, err
			}
			spec.planBytes, err = json.MarshalIndent(spec.plan, "", "  ")
			if err != nil {
				return nil, err
			}
			spec.planBytes = append(spec.planBytes, '\n')
			spec.source.SHA256 = tool.DigestBytes(spec.planBytes)
		}
		if spec.source.Kind == "plan" {
			err = tool.CopyTree(historySeed, filepath.Join(baseline, "history-seed"))
		}
		if err != nil {
			return nil, err
		}
	}
	session := &Session{
		SchemaVersion:    SchemaVersion,
		SessionID:        sessionID,
		Tool:             self,
		ToolVersion:      version,
		ToolSHA256:       toolHash,
		AgentType:        agentType,
		Source:           spec.source,
		HistorySeed:      historySeed,
		HistorySeedFiles: historySeedFiles,
		ResourceRoot:     root,
		Workspace:        filepath.Join(baseline, "workspace"),
		Home:             filepath.Join(baseline, "home"),
		Config:           filepath.Join(baseline, "config"),
		Tmp:              filepath.Join(baseline, "tmp"),
		ProtocolCwd:      planProtocolCwd(spec.plan, filepath.Join(baseline, "workspace")),
		Env:              baselineEnv,
		Routes:           map[string]Route{},
		Limits: Limits{
			ProtocolBytes: o.ProtocolBytes,
			StderrBytes:   o.StderrBytes,
			MaxFrames:     o.MaxFrames,
			MaxFrameBytes: o.MaxFrameBytes,
		},
	}
	if err := writePrivateEnvironment(root, privateEnv); err != nil {
		return nil, err
	}
	if spec.source.Kind == "case" || spec.source.Kind == "plan" {
		path, err := writePlanSnapshot(sessionDir, spec.planBytes)
		if err != nil {
			return nil, err
		}
		session.Source.Path = path
	}
	ids := ConnectionIDs(session.Source, spec.plan)
	for _, action := range []string{"record", "mock"} {
		if !spec.routes[action] {
			session.Routes[action] = Route{Enabled: false}
			continue
		}
		routeRoot := filepath.Join(root, "routes", action)
		if err := os.MkdirAll(routeRoot, 0o700); err != nil {
			return nil, err
		}
		templateRoot := filepath.Join(root, "templates", action)
		if err := tool.CopyTree(baseline, templateRoot); err != nil {
			return nil, err
		}
		if action == "mock" {
			if err := cleanMockConfig(templateRoot, spec.plan, agentType); err != nil {
				return nil, err
			}
		}
		if err := bindRouteSlots(routeRoot, templateRoot); err != nil {
			return nil, err
		}
		route := Route{
			Enabled:     true,
			Executor:    map[string]string{"record": "external-sdk", "mock": "external-sdk"}[action],
			Target:      map[string]string{"record": "native-agent", "mock": "replay"}[action],
			Provenance:  session.Source.Provenance,
			Workspace:   filepath.Join(routeRoot, "workspace"),
			ProtocolCwd: planProtocolCwd(spec.plan, filepath.Join(routeRoot, "workspace")),
			Env:         baseEnvironment(agentType, routeRoot),
		}
		if action == "record" {
			route.RuntimeEnv = environmentNames(privateEnv)
			route.Provenance, err = executionProvenance(session, action, nil)
			if err != nil {
				return nil, err
			}
		}
		for _, id := range ids {
			template := ConnectionTemplate{ID: id, Command: self.Command, Args: []string{"agent", "mock", "--session-dir", sessionDir, "--connection", id}}
			switch action {
			case "record":
				template.Args = []string{"agent", "record", "--session-dir", sessionDir, "--cmd"}
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
	materialID, err := randomID()
	if err != nil {
		return nil, err
	}
	if spec.material != nil {
		if err := validateExecutionID(spec.material.ID); err != nil {
			return nil, err
		}
		materialID = spec.material.ID
	}
	materialRoot := filepath.Join(sessionDir, "materials", materialID)
	var materialArtifact *corpus.Material
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
		if err := copyMaterial(session.Source.Path, materialRoot); err != nil {
			return nil, err
		}
		session.Source.Path = materialRoot
		if spec.material != nil {
			imported := *spec.material
			imported.Path = materialRoot
			materialArtifact = &imported
		} else {
			artifact, err := artifactForMaterial(materialRoot, session.Source.Provenance, "initial", ids)
			if err != nil {
				return nil, err
			}
			materialArtifact = artifact
		}
	}
	if materialArtifact != nil {
		if spec.material == nil {
			materialArtifact.ID = materialID
			materialArtifact.Action = "mock"
		}
		files, err := history.HashTree(materialRoot)
		if err != nil {
			return nil, err
		}
		delete(files, "material.json")
		materialArtifact.Files = files
		if err := writeAtomic(filepath.Join(materialRoot, "material.json"), materialArtifact); err != nil {
			return nil, err
		}
		if _, err := inspectMaterial(session, *materialArtifact); err != nil {
			return nil, err
		}
	}
	if _, err := LoadPlan(session); err != nil {
		return nil, err
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	sessionPath := SessionPath(sessionDir)
	if err := writeExclusive(sessionPath, session); err != nil {
		return nil, err
	}
	sessionHash, err := tool.FileDigest(sessionPath)
	if err != nil {
		return nil, err
	}
	index := &Index{SchemaVersion: SchemaVersion, SessionID: session.SessionID, SessionSHA256: sessionHash, LastUpdatedUTC: now()}
	if materialArtifact != nil {
		index.Artifacts = append(index.Artifacts, *materialArtifact)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "materials"), 0o700); err != nil {
		return nil, err
	}
	if err := writeMaterialIndex(sessionDir, index); err != nil {
		return nil, err
	}
	if err := writeExclusive(IndexPath(sessionDir), index); err != nil {
		return nil, err
	}
	currentID := ""
	if materialArtifact != nil {
		currentID = materialArtifact.ID
	}
	if err := writeCurrent(sessionDir, session, currentID); err != nil {
		return nil, err
	}
	if err := os.Chmod(sessionPath, 0o400); err != nil {
		return nil, err
	}
	lock, err := actionLock(sessionDir, true)
	if err != nil {
		return nil, err
	}
	if err := lock.Close(); err != nil {
		return nil, err
	}
	return session, nil
}

// copyMaterial checks the entire closure on both sides of the copy. In
// particular, files not covered by a legacy manifest still get immutable hashes.
func copyMaterial(source, target string) error {
	before, err := history.HashTree(source)
	if err != nil {
		return err
	}
	if err := tool.CopyTree(source, target); err != nil {
		return err
	}
	after, err := history.HashTree(target)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("material changed while copying into session")
	}
	return nil
}
