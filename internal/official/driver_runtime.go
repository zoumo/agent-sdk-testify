package official

import (
	"errors"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const driverRuntimeSchemaVersion = 1

func newDriverRuntime(root, workspace, home, config, raw, tmp, seedRoot string, replay bool) tool.Object {
	runtime := tool.Object{
		"schemaVersion": float64(driverRuntimeSchemaVersion),
		"root":          root,
		"workspace":     workspace,
		"home":          home,
		"config":        config,
		"raw":           raw,
		"tmp":           tmp,
		"output":        root,
		"replay":        replay,
	}
	if seedRoot != "" {
		runtime["seedRoot"] = seedRoot
	}
	return runtime
}

// The sealed native cwd is a protocol identity, never a location to reopen.
// Claude may use its physical cwd in child Read requests even when the plan
// carries the stable record route. That route no longer names the same overlay
// after recording finishes.
func bindRecordedWorkspace(runtime tool.Object, material corpus.Material, agent string) error {
	if material.FormatVersion != 3 || agent != "claude" {
		return nil
	}
	var manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(material.Path, "manifest.json"), &manifest); err != nil {
		return err
	}
	workspace := tool.Str(tool.Obj(manifest["native"])["cwd"])
	if workspace == "" || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return errors.New("sealed native cwd must be an absolute clean path")
	}
	runtime["recordedWorkspace"] = workspace
	return nil
}

func validateDriverRuntime(runtime tool.Object) error {
	if runtime["schemaVersion"] != float64(driverRuntimeSchemaVersion) {
		return errors.New("official driver requires runtime configuration schema 1")
	}
	for _, name := range []string{"root", "workspace", "home", "config", "raw", "tmp", "output"} {
		if value := tool.Str(runtime[name]); value == "" || !filepath.IsAbs(value) {
			return errors.New("official driver runtime configuration omits an absolute " + name)
		}
	}
	if _, ok := runtime["replay"].(bool); !ok {
		return errors.New("official driver runtime configuration omits replay")
	}
	if workspace, exists := runtime["recordedWorkspace"]; exists {
		value := tool.Str(workspace)
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return errors.New("official driver recordedWorkspace must be an absolute clean path")
		}
	}
	return nil
}
