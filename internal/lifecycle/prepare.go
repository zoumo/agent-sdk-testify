package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/native"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validatePreparePaths(fixtureDir string, source *sourceSpec) (string, error) {
	if !filepath.IsAbs(fixtureDir) {
		return "", errors.New("--fixture-dir must be an absolute path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(fixtureDir))
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, filepath.Base(fixtureDir))
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		if err == nil {
			return "", fmt.Errorf("--fixture-dir already exists: %s", dir)
		}
		return "", err
	}
	if source.source.Kind == "recording" {
		rel, err := filepath.Rel(source.source.Path, dir)
		if err != nil || filepath.IsLocal(rel) {
			return "", errors.New("--fixture-dir must be outside the recording")
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
	return bindPreparedRouteSlots(routeRoot, targetRoot, targetRoot)
}

// Validation uses the physical tree; links name its final published location.
func bindPreparedRouteSlots(routeRoot, targetRoot, publishedTarget string) error {
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
		if err := os.Symlink(filepath.Join(publishedTarget, name), temp); err != nil {
			return err
		}
		if err := os.Rename(temp, filepath.Join(routeRoot, name)); err != nil {
			_ = os.Remove(temp)
			return err
		}
	}
	return nil
}

func restoreRouteSlots(fixture *Fixture, action string) error {
	return bindRouteSlots(
		filepath.Join(fixture.ResourceRoot, "routes", action),
		filepath.Join(fixture.ResourceRoot, "templates", action),
	)
}

func cleanMockConfig(root string, plan tool.Object, provider string) error {
	profile, err := agentProfileFor(provider)
	if err != nil {
		return err
	}
	return profile.writeConfig(root, plan)
}

func writeConnectionLauncher(path, id, self, fixtureDir string) error {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	invocation := strings.Join([]string{quote(self), quote("agent"), quote("mock"), quote("--fixture-dir"), quote(fixtureDir), quote("--connection"), quote(id)}, " ")
	script := "#!/bin/sh\nexec " + invocation + " -- \"$@\"\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(script), 0o700)
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

func writePlanSnapshot(fixtureDir string, raw []byte) (string, error) {
	path := filepath.Join(fixtureDir, "source", "plan.json")
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

func initialBuiltin(fixture *Fixture, fixtureDir, materialID string, ids []string) (*corpus.Material, error) {
	if fixture.Source.Kind != "case" {
		return nil, nil
	}
	material, _, available, err := builtin.SelectOptional(fixture.AgentType, fixture.Source.ID)
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
	core, completePackage, err := corpus.RecordingRoot(extracted)
	if err != nil {
		return nil, err
	}
	descriptor, err := corpus.ReadMaterialDescriptor(core)
	if err != nil {
		return nil, err
	}
	if descriptor != nil {
		if err := validateExecutionID(descriptor.ID); err != nil {
			return nil, err
		}
		materialID = descriptor.ID
	}
	materialRoot := MaterialRoot(fixtureDir, materialID)
	target := materialRoot
	if completePackage {
		target = PackageRoot(fixtureDir, materialID)
	}
	if err := copyMaterial(extracted, target); err != nil {
		return nil, err
	}
	if descriptor != nil {
		descriptor.Path = materialRoot
		return descriptor, nil
	}
	artifact, err := artifactForMaterial(materialRoot, "builtin", ids)
	if err != nil {
		return nil, err
	}
	artifact.ID = materialID
	return artifact, nil
}

// Prepare creates a caller-owned fixture directory containing its runtime
// baseline. It never starts a client, SDK, driver or native Agent.
func Prepare(o PrepareOptions) (*Fixture, error) {
	return prepare(o, nil)
}

// beforePublish is an internal fault boundary; all preparation has completed
// when invoked, and only the final no-replace rename remains.
func prepare(o PrepareOptions, beforePublish func(stage, target string) error) (result *Fixture, err error) {
	spec, err := resolveSource(o)
	if err != nil {
		return nil, err
	}
	fixtureDir, err := validatePreparePaths(o.FixtureDir, spec)
	if err != nil {
		return nil, err
	}
	toolIdentity, err := prepareToolIdentity(o.EmbeddedTool, o.EmbeddedIdentity)
	if err != nil {
		return nil, err
	}
	publishedDir := fixtureDir
	fixtureDir, err = os.MkdirTemp(filepath.Dir(publishedDir), ".agent-sdk-testify-prepare-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(fixtureDir)
	var root string
	defer func() {
		if err != nil {
			diagnostic, createErr := os.CreateTemp(filepath.Dir(publishedDir), ".agent-sdk-testify-prepare-failed-*.json")
			if createErr == nil {
				name := diagnostic.Name()
				_ = diagnostic.Close()
				_ = tool.WriteJSON(name, tool.Object{"schemaVersion": SchemaVersion, "status": "failed", "error": err.Error()})
			}
		}
	}()
	root = filepath.Join(fixtureDir, "runtime")
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, err
	}
	if toolIdentity.Source == ToolSourceExecutable {
		if err := tool.WriteJSON(ToolBindingPath(fixtureDir), Command{
			Command: toolIdentity.Command,
			Args:    slices.Clone(toolIdentity.Args),
		}); err != nil {
			return nil, err
		}
	}
	baseline := filepath.Join(root, "baseline")
	for _, name := range []string{"workspace", "home", "config", "tmp", "history-seed"} {
		if err := os.MkdirAll(filepath.Join(baseline, name), 0o700); err != nil {
			return nil, err
		}
	}
	fixtureID, err := randomID()
	if err != nil {
		return nil, err
	}
	agentType := spec.agentType
	if err := validateAgent(agentType); err != nil {
		return nil, err
	}
	baselineEnv := baseEnvironment(agentType, baseline)
	privateEnv := map[string]string{}
	if err := native.CopyNativeConfig(agentType, filepath.Join(baseline, "config"), filepath.Join(BaselineRoot(publishedDir), "config"), filepath.Join(root, "private-config"), privateEnv); err != nil {
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
		rel, err := filepath.Rel(sourceSeed, fixtureDir)
		if err != nil || filepath.IsLocal(rel) {
			return nil, errors.New("--fixture-dir must be outside the history seed")
		}
		historySeed = SourceSeedPath(fixtureDir)
		seedCore, _, err := corpus.RecordingRoot(sourceSeed)
		if err != nil {
			return nil, err
		}
		if err := corpus.VerifyMaterials(seedCore, true); err != nil {
			return nil, err
		}
		if err := copyMaterial(sourceSeed, filepath.Join(historySeed, "source")); err != nil {
			return nil, err
		}
		var seedPlan tool.Object
		if err := tool.ReadJSON(filepath.Join(history.SeedSource(historySeed), "plan.json"), &seedPlan); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Join(historySeed, "project"), 0o700); err != nil {
			return nil, err
		}
		if err := tool.WriteFiles(filepath.Join(historySeed, "project"), tool.Obj(tool.Obj(seedPlan["scenario"])["files"])); err != nil {
			return nil, err
		}
		files, err := history.HashTree(historySeed)
		if err != nil {
			return nil, err
		}
		historySeedFiles = map[string]string{}
		for name, digest := range files {
			historySeedFiles[fixtureSourceSeed+"/"+name] = digest
		}
		if spec.source.Kind == "plan" {
			if _, err := history.MaterializeSeed(historySeed, baseline, spec.plan); err != nil {
				return nil, err
			}
		}
		if spec.source.Kind == "case" && spec.plan["driverProfile"] == "existing-children" {
			if err := native.PrepareHistorySeed(historySeed, baseline, spec.plan); err != nil {
				return nil, err
			}
		}

		if err != nil {
			return nil, err
		}
	}
	fixture := &Fixture{
		SchemaVersion:    SchemaVersion,
		FixtureID:        fixtureID,
		Tool:             toolIdentity,
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
	fixture.dir = fixtureDir
	if err := writePrivateEnvironment(root, privateEnv); err != nil {
		return nil, err
	}
	if spec.source.Kind == "case" || spec.source.Kind == "plan" {
		path, err := writePlanSnapshot(fixtureDir, spec.planBytes)
		if err != nil {
			return nil, err
		}
		fixture.Source.Path = path
	}
	ids := ConnectionIDs(fixture.Source, spec.plan)
	for _, action := range []string{"record", "mock"} {
		if !spec.routes[action] {
			fixture.Routes[action] = Route{Enabled: false}
			continue
		}
		routeRoot := RouteRoot(fixtureDir, action)
		if err := os.MkdirAll(routeRoot, 0o700); err != nil {
			return nil, err
		}
		templateRoot := TemplateRoot(fixtureDir, action)
		if err := tool.CopyTree(baseline, templateRoot); err != nil {
			return nil, err
		}
		if action == "mock" {
			if err := cleanMockConfig(templateRoot, spec.plan, agentType); err != nil {
				return nil, err
			}
		}
		if err := bindPreparedRouteSlots(routeRoot, templateRoot, TemplateRoot(publishedDir, action)); err != nil {
			return nil, err
		}
		route := Route{
			Enabled:     true,
			Executor:    map[string]string{"record": "external-sdk", "mock": "external-sdk"}[action],
			Target:      map[string]string{"record": "native-agent", "mock": "replay"}[action],
			Provenance:  fixture.Source.Provenance,
			Workspace:   RouteWorkspace(fixtureDir, action),
			ProtocolCwd: planProtocolCwd(spec.plan, RouteWorkspace(fixtureDir, action)),
			Env:         baseEnvironment(agentType, routeRoot),
		}
		if action == "record" {
			route.RuntimeEnv = environmentNames(privateEnv)
			route.Provenance, err = executionProvenance(fixture, action, nil)
			if err != nil {
				return nil, err
			}
		}
		for _, id := range ids {
			template := ConnectionTemplate{ID: id, Transport: ConnectionTransportInProcess}
			if !o.EmbeddedTool {
				template.Transport = ConnectionTransportProcess
				template.Command = toolIdentity.Command
				switch action {
				case "record":
					template.Args = []string{"agent", "record", "--cmd"}
				case "mock":
					template.Args = []string{"agent", "mock", "--connection", id}
					template.Launcher = LauncherPath(fixtureDir, id)
					if err := writeConnectionLauncher(template.Launcher, id, toolIdentity.Command, publishedDir); err != nil {
						return nil, err
					}
				}
			}
			route.Connections = append(route.Connections, template)
		}
		fixture.Routes[action] = route
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
	materialRoot := MaterialRoot(fixtureDir, materialID)
	var materialArtifact *corpus.Material
	descriptorlessImport := fixture.Source.Kind == "recording" && spec.material == nil
	switch fixture.Source.Kind {
	case "case":
		artifact, err := initialBuiltin(fixture, fixtureDir, materialID, ids)
		if err != nil {
			return nil, err
		}
		if artifact != nil {
			materialID = artifact.ID
			materialRoot = MaterialRoot(fixtureDir, materialID)
			_, statErr := os.Lstat(filepath.Join(materialRoot, "material.json"))
			descriptorlessImport = os.IsNotExist(statErr)
			if !descriptorlessImport {
				spec.material = artifact
			}
			materialArtifact = artifact
		}
	case "recording":
		importRoot := spec.source.Path
		if spec.packageRoot != "" {
			importRoot = spec.packageRoot
			if err := copyMaterial(importRoot, PackageRoot(fixtureDir, materialID)); err != nil {
				return nil, err
			}
		} else if err := copyMaterial(importRoot, materialRoot); err != nil {
			return nil, err
		}

		rawSource, err := os.ReadFile(filepath.Join(materialRoot, "source-plan.json"))
		if os.IsNotExist(err) {
			rawSource, err = os.ReadFile(filepath.Join(materialRoot, "plan.json"))
		}
		if err != nil {
			return nil, err
		}
		sourcePath, err := writePlanSnapshot(fixtureDir, rawSource)
		if err != nil {
			return nil, err
		}
		fixture.Source.Path = sourcePath
		fixture.Source.SHA256 = tool.DigestBytes(rawSource)
		seedPath := filepath.Join(materialRoot, "history", "seed")
		if info, err := os.Lstat(seedPath); err == nil && info.IsDir() {
			fixture.HistorySeed = SourceSeedPath(fixtureDir)
			if err := copyMaterial(seedPath, fixture.HistorySeed); err != nil {
				return nil, err
			}
			hashes, err := history.HashTree(fixture.HistorySeed)
			if err != nil {
				return nil, err
			}
			fixture.HistorySeedFiles = map[string]string{}
			for name, hash := range hashes {
				fixture.HistorySeedFiles[fixtureSourceSeed+"/"+name] = hash
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}

		if spec.material != nil {
			imported := *spec.material
			imported.Path = materialRoot
			materialArtifact = &imported
		} else {
			artifact, err := artifactForMaterial(materialRoot, fixture.Source.Provenance, ids)
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
			if descriptorlessImport {
				materialArtifact.FormatVersion = 0
			} else {
				materialArtifact.FormatVersion = 2
			}
		}
		files, err := history.HashTree(materialRoot)
		if err != nil {
			return nil, err
		}
		delete(files, "material.json")
		if materialArtifact.FormatVersion == 3 {
			materialArtifact.Files = nil
		} else if len(materialArtifact.Files) == 0 {
			materialArtifact.Files = files
			if descriptorlessImport {
				materialArtifact.LegacyInventorySHA256 = history.InventoryDigest(files)
			}
		} else if !reflect.DeepEqual(materialArtifact.Files, files) {
			return nil, errors.New("imported material closure differs from its descriptor")
		}
		if spec.material == nil && !descriptorlessImport {
			if err := writeAtomic(filepath.Join(materialRoot, "material.json"), materialArtifact); err != nil {
				return nil, err
			}
		}
		if _, err := inspectMaterialWithDescriptor(fixture, *materialArtifact, !descriptorlessImport); err != nil {
			return nil, err
		}
	}
	if _, err := LoadPlan(fixture); err != nil {
		return nil, err
	}
	if err := fixture.Validate(); err != nil {
		return nil, err
	}
	fixturePath := FixturePath(fixtureDir)
	if err := writeExclusive(fixturePath, fixture); err != nil {
		return nil, err
	}
	fixtureHash, err := tool.FileDigest(fixturePath)
	if err != nil {
		return nil, err
	}
	index := &Index{SchemaVersion: SchemaVersion, FixtureID: fixture.FixtureID, FixtureSHA256: fixtureHash, Descriptorless: map[string]bool{}, LastUpdatedUTC: now()}
	if materialArtifact != nil {
		index.Artifacts = append(index.Artifacts, *materialArtifact)
		if descriptorlessImport {
			index.Descriptorless[materialArtifact.ID] = true
		}
	}
	if err := os.MkdirAll(filepath.Join(fixtureDir, "packages"), 0o700); err != nil {
		return nil, err
	}
	if err := writeMaterialIndex(fixtureDir, index); err != nil {
		return nil, err
	}
	if err := writeExclusive(IndexPath(fixtureDir), index); err != nil {
		return nil, err
	}
	currentID := ""
	if materialArtifact != nil {
		currentID = materialArtifact.ID
	}
	if err := writeCurrent(fixtureDir, fixture, currentID); err != nil {
		return nil, err
	}
	if err := refreshPreparedRoutes(fixture, publishedDir); err != nil {
		return nil, err
	}
	if err := os.Chmod(fixturePath, 0o400); err != nil {
		return nil, err
	}
	lock, err := actionLock(fixtureDir, true)
	if err != nil {
		return nil, err
	}
	if err := lock.Close(); err != nil {
		return nil, err
	}
	if beforePublish != nil {
		if err := beforePublish(fixtureDir, publishedDir); err != nil {
			return nil, err
		}
	}
	if err := renameExport(fixtureDir, publishedDir); err != nil {
		return nil, err
	}
	// Resolve from the committed descriptor, so the returned object has exactly
	// the same final runtime bindings as every subsequent Open.
	return OpenFixture(publishedDir)
}

func prepareToolIdentity(embedded bool, provided *version.EmbeddedIdentity) (ToolIdentity, error) {
	if embedded {
		build, err := version.ResolveEmbeddedIdentity(provided)
		if err != nil {
			return ToolIdentity{}, err
		}
		return ToolIdentity{
			Source:         ToolSourceEmbedded,
			Version:        build.Version,
			Module:         build.Module,
			ModuleSum:      build.Sum,
			ModuleRevision: build.Revision,
			ModuleTime:     build.Time,
			GoVersion:      build.GoVersion,
		}, nil
	}
	command, toolVersion, hash, err := SelfCommand()
	if err != nil {
		return ToolIdentity{}, err
	}
	return ToolIdentity{
		Source:  ToolSourceExecutable,
		Version: toolVersion,
		Command: command.Command,
		Args:    append([]string(nil), command.Args...),
		SHA256:  hash,
	}, nil
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
		return errors.New("material changed while copying into fixture")
	}
	return nil
}
