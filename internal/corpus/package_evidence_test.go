package corpus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPackageMockIsolationValidatesItsOwnExecution(t *testing.T) {
	root := t.TempDir()
	material := Material{ID: "material", Connections: []string{"main"}}
	executionID := "replay"
	overlay := filepath.Join(root, "runtime", "executions", executionID)
	plan := tool.Object{"provider": "codex", "project": "/recorded/project"}
	declaration := tool.Object{"fixtureId": "fixture", "nativeArgs": []any{"app-server"}}
	path := filepath.Join(root, "executions", executionID, "execution.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, tool.WriteJSON(path, declaration))
	hash, err := tool.FileDigest(path)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "material"), 0o700))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "material", "plan.json"), plan))
	domain := filepath.Join(root, "executions", executionID)
	require.NoError(t, os.MkdirAll(domain, 0o700))
	preparedPath := filepath.Join(domain, "prepared-plan.json")
	prepared := tool.Object{"schemaVersion": 3, "fixtureId": "fixture", "executionId": executionID, "materialId": material.ID, "command": "mock", "provider": "codex", "sourcePlanSHA256": "", "planSHA256": "", "manifestSHA256": "", "args": declaration["nativeArgs"], "root": overlay, "project": plan["project"], "home": filepath.Join(overlay, "home"), "config": filepath.Join(overlay, "config"), "tmp": filepath.Join(overlay, "tmp")}
	require.NoError(t, tool.WriteJSON(preparedPath, prepared))
	preparedHash, err := tool.FileDigest(preparedPath)
	require.NoError(t, err)
	valid := func() tool.Object {
		return tool.Object{
			"method": "mock-preparation", "preparedPlan": tool.Object{"path": "executions/replay/prepared-plan.json", "sha256": preparedHash},
			"schemaVersion": 3, "fixtureId": "fixture", "executionId": executionID, "materialId": material.ID,
			"execution": tool.Object{"path": "executions/replay/execution.json", "sha256": hash},
			"command":   "mock", "nativeArgs": declaration["nativeArgs"],
			"workspace": filepath.Join(overlay, "workspace"), "protocolCwd": plan["project"],
			"home": filepath.Join(overlay, "home"), "config": filepath.Join(overlay, "config"), "tmp": filepath.Join(overlay, "tmp"),
			"environment": tool.Object{
				"HOME": filepath.Join(overlay, "home"), "CODEX_HOME": filepath.Join(overlay, "config"),
				"TMPDIR": filepath.Join(overlay, "tmp"), "XDG_CONFIG_HOME": filepath.Join(overlay, "home", ".config"),
				"XDG_CACHE_HOME": filepath.Join(overlay, "home", ".cache"), "XDG_DATA_HOME": filepath.Join(overlay, "home", ".local", "share"),
			},
			"connections": []any{tool.Object{"connectionId": "main", "executionId": executionID, "executionSHA256": hash}},
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(tool.Object)
	}{
		{"valid", func(tool.Object) {}},
		{"missing version", func(o tool.Object) { delete(o, "schemaVersion") }},
		{"old version", func(o tool.Object) { o["schemaVersion"] = 2 }},
		{"unknown version", func(o tool.Object) { o["schemaVersion"] = 4 }},
		{"other fixture", func(o tool.Object) { o["fixtureId"] = "other" }},
		{"other execution", func(o tool.Object) { o["executionId"] = "other" }},
		{"other material", func(o tool.Object) { o["materialId"] = "other" }},
		{"wrong argv", func(o tool.Object) { o["nativeArgs"] = []any{"other"} }},
		{"wrong prepared plan hash", func(o tool.Object) { tool.Obj(o["preparedPlan"])["sha256"] = "wrong" }},
		{"wrong declaration hash", func(o tool.Object) { tool.Obj(o["execution"])["sha256"] = "wrong" }},
		{"escaping reference", func(o tool.Object) { tool.Obj(o["execution"])["path"] = "../execution.json" }},
		{"external workspace", func(o tool.Object) { o["workspace"] = "/external/workspace" }},
		{"changed protocol cwd", func(o tool.Object) { o["protocolCwd"] = filepath.Join(overlay, "workspace") }},
		{"external home", func(o tool.Object) { tool.Obj(o["environment"])["HOME"] = "/personal" }},
		{"external config", func(o tool.Object) { tool.Obj(o["environment"])["CODEX_HOME"] = "/personal" }},
		{"wrong directory", func(o tool.Object) { o["tmp"] = "/tmp" }},
		{"missing connection", func(o tool.Object) { o["connections"] = []any{} }},
		{"wrong connection", func(o tool.Object) { tool.Obj(tool.Array(o["connections"])[0])["connectionId"] = "probe" }},
		{"borrowed execution", func(o tool.Object) { tool.Obj(tool.Array(o["connections"])[0])["executionId"] = "prior" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolation := valid()
			tc.mutate(isolation)
			require.NoError(t, tool.WriteJSON(filepath.Join(domain, "isolation.json"), isolation))
			err := validatePackageMockIsolation(root, executionID, material, declaration)
			if tc.name == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestLegacyPreparationHasOneExplicitUnversionedShape(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"isolation.json", "prepared-plan.json"} {
		require.NoError(t, tool.WriteJSON(filepath.Join(root, name), tool.Object{"schemaVersion": 1}))
	}
	isolationHash, err := tool.FileDigest(filepath.Join(root, "isolation.json"))
	require.NoError(t, err)
	preparedHash, err := tool.FileDigest(filepath.Join(root, "prepared-plan.json"))
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		mutate func(tool.Object)
	}{
		{"legacy", func(tool.Object) {}},
		{"version one", func(o tool.Object) { o["schemaVersion"] = float64(1) }},
		{"null version", func(o tool.Object) { o["schemaVersion"] = nil }},
		{"mixed fields", func(o tool.Object) { o["material"] = tool.Object{"id": "other"} }},
		{"missing hash", func(o tool.Object) { delete(o, "isolationSHA256") }},
		{"wrong isolation", func(o tool.Object) { o["isolationSHA256"] = "wrong" }},
		{"wrong preparation", func(o tool.Object) { o["preparedPlanSHA256"] = "wrong" }},
		{"wrong execution", func(o tool.Object) { o["executionId"] = "other" }},
		{"unknown version", func(o tool.Object) { o["schemaVersion"] = 4 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preparation := tool.Object{"executionId": "capture", "isolationSHA256": isolationHash, "preparedPlanSHA256": preparedHash}
			tc.mutate(preparation)
			v := &Validation{}
			v.capturePreparation(root, root, "", tool.Object{"executionId": "capture"}, nil, preparation)
			if tc.name == "legacy" || tc.name == "version one" {
				require.NoError(t, v.Err)
			} else {
				require.Error(t, v.Err)
			}
		})
	}
}

func TestPackageQualificationRejectsMissingMaterialDescriptor(t *testing.T) {
	root := t.TempDir()
	material := filepath.Join(root, "material")
	replay := filepath.Join(root, "verify", "verification")
	require.NoError(t, os.MkdirAll(material, 0o700))
	c := &Corpus{}
	_, err := c.qualifyPackage(material, replay, false)
	require.ErrorContains(t, err, "material descriptor")
	_, err = c.ValidateAdmission(material, replay, tool.Object{"schemaVersion": float64(2)})
	require.ErrorContains(t, err, "material descriptor")
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "adoption.json"), tool.Object{"schemaVersion": 2}))
	_, err = VerifyAdoptedFiles(root)
	require.ErrorContains(t, err, "material descriptor")
}
