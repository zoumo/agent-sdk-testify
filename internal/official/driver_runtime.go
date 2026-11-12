package official

import (
	"errors"
	"path/filepath"

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
	return nil
}
