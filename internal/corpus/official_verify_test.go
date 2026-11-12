package corpus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestMockIsolationSchemaTwoValidatesAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	executionID := "exec-1"
	overlay := filepath.Join(root, "executions", executionID)
	material := tool.Object{"id": "material", "planSHA256": "plan"}
	evidence := tool.Object{
		"fixtureId": "fixture-1",
		"material":  material,
		"files": tool.Object{
			"driver/mock-isolation.json":             "",
			"driver/connections/main/execution.json": "execution-digest",
		},
		"mockIsolation": tool.Object{"path": "driver/mock-isolation.json"},
	}
	plan := tool.Object{"provider": "codex", "project": "/recorded/project"}
	executions := map[string]tool.Object{executionID: {"nativeArgs": []any{"exec", "--sandbox", "read-only"}}}
	valid := func() tool.Object {
		return tool.Object{
			"schemaVersion": float64(2), "fixtureId": "fixture-1", "executionId": executionID,
			"execution":   tool.Object{"path": "executions/" + executionID + "/execution.json", "sha256": "execution-digest"},
			"material":    material,
			"command":     "mock",
			"nativeArgs":  []any{"exec", "--sandbox", "read-only"},
			"workspace":   filepath.Join(overlay, "workspace"),
			"protocolCwd": "/recorded/project",
			"home":        filepath.Join(overlay, "home"),
			"config":      filepath.Join(overlay, "config"),
			"tmp":         filepath.Join(overlay, "tmp"),
			"environment": tool.Object{
				"HOME":            filepath.Join(overlay, "home"),
				"CODEX_HOME":      filepath.Join(overlay, "config"),
				"TMPDIR":          filepath.Join(overlay, "tmp"),
				"XDG_CONFIG_HOME": filepath.Join(overlay, "home", ".config"),
				"XDG_CACHE_HOME":  filepath.Join(overlay, "home", ".cache"),
				"XDG_DATA_HOME":   filepath.Join(overlay, "home", ".local", "share"),
			},
		}
	}
	write := func(isolation tool.Object) {
		path := filepath.Join(root, "driver", "mock-isolation.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, tool.WriteJSON(path, isolation))
		digest, err := tool.FileDigest(path)
		require.NoError(t, err)
		tool.Obj(evidence["files"])["driver/mock-isolation.json"] = digest
		tool.Obj(evidence["mockIsolation"])["sha256"] = digest
	}
	validate := func() *Validation {
		v := &Validation{}
		v.validateMockIsolation(root, plan, evidence, executions)
		return v
	}

	write(valid())
	require.NoError(t, validate().Err)

	for _, tc := range []struct {
		name   string
		mutate func(tool.Object)
	}{
		{"unknown version", func(isolation tool.Object) { isolation["schemaVersion"] = float64(3) }},
		{"unknown execution", func(isolation tool.Object) { isolation["executionId"] = "exec-2" }},
		{"different material", func(isolation tool.Object) { isolation["material"] = tool.Object{"id": "other"} }},
		{"different native args", func(isolation tool.Object) { isolation["nativeArgs"] = []any{"exec"} }},
		{"replaced protocol cwd", func(isolation tool.Object) { isolation["protocolCwd"] = "/attempt/workspace" }},
		{"workspace outside overlay", func(isolation tool.Object) { isolation["workspace"] = filepath.Join(root, "workspace") }},
		{"escaping environment", func(isolation tool.Object) { tool.Obj(isolation["environment"])["HOME"] = "/tmp" }},
		{"wrong execution reference", func(isolation tool.Object) {
			tool.Obj(isolation["execution"])["sha256"] = "other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolation := valid()
			tc.mutate(isolation)
			write(isolation)
			require.Error(t, validate().Err)
		})
	}
}
