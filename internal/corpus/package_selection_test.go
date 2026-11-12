package corpus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func sealedPackage(t *testing.T) (string, Material) {
	t.Helper()
	m := formatThreeCore(t)
	root := t.TempDir()
	require.NoError(t, os.Rename(m.Path, filepath.Join(root, "material")))
	m.Path = filepath.Join(root, "material")
	require.NoError(t, tool.WriteJSON(filepath.Join(m.Path, "material.json"), m))
	materialHash, err := tool.FileDigest(filepath.Join(m.Path, "material.json"))
	require.NoError(t, err)
	seal := func(prefix, envelope string, object tool.Object) PackageSelectionReference {
		dir := filepath.Join(root, prefix)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stderr"), nil, 0o600))
		files, err := history.HashTree(dir)
		require.NoError(t, err)
		inventory := map[string]string{}
		for name, hash := range files {
			inventory[prefix+"/"+name] = hash
		}
		object["files"] = inventory
		ref := prefix + "/" + envelope
		require.NoError(t, tool.WriteJSON(filepath.Join(root, ref), object))
		hash, err := tool.FileDigest(filepath.Join(root, ref))
		require.NoError(t, err)
		return PackageSelectionReference{Path: ref, SHA256: hash}
	}
	executions := []PackageSelectionReference{}
	for _, id := range []string{"capture", "replay"} {
		prefix := "executions/" + id
		require.NoError(t, os.MkdirAll(filepath.Join(root, prefix), 0o700))
		descriptor := prefix + "/execution.json"
		require.NoError(t, tool.WriteJSON(filepath.Join(root, descriptor), tool.Object{
			"schemaVersion": 7, "executionId": id, "artifact": tool.Object{"id": m.ID},
		}))
		require.NoError(t, tool.WriteJSON(filepath.Join(root, prefix, "materialization.json"), tool.Object{"version": 2, "transformations": []any{}}))
		hash, err := tool.FileDigest(filepath.Join(root, descriptor))
		require.NoError(t, err)
		executions = append(executions, seal(prefix, "result.json", tool.Object{
			"schemaVersion": 1, "executionId": id, "materialId": m.ID, "status": "passed",
			"execution": descriptor, "executionSHA256": hash,
			"material": "material/material.json", "materialSHA256": materialHash,
			"planSHA256": m.PlanSHA256, "sourcePlanSHA256": m.SourcePlanSHA256, "manifestSHA256": m.ManifestSHA256,
		}))
	}
	record := seal("record/run", "result.json", tool.Object{"schemaVersion": 3, "id": "run", "materialId": m.ID, "status": "passed", "executionId": "capture"})
	verify := seal("verify/verification", "verification.json", tool.Object{
		"schemaVersion": 3, "id": "verification", "materialId": m.ID, "status": "passed", "executions": []any{tool.Object{"executionId": "replay"}},
	})
	review := PackageSelection{SchemaVersion: 2, MaterialID: m.ID, Material: PackageSelectionReference{Path: "material/material.json", SHA256: materialHash}, Record: record, Verification: verify, Executions: executions}
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "review.json"), review))
	hash, err := tool.FileDigest(filepath.Join(root, "review.json"))
	require.NoError(t, err)
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "adoption.json"), tool.Object{"schemaVersion": 2, "materialId": m.ID, "adoption": "admitted", "reviewSHA256": hash}))
	return root, m
}

func TestPackageSelectionBindsExactClosedDomains(t *testing.T) {
	root, m := sealedPackage(t)
	before, err := history.HashTree(root)
	require.NoError(t, err)
	require.NoError(t, ValidatePackage(root, m))
	core, full, err := RecordingRoot(root)
	require.NoError(t, err)
	require.True(t, full)
	require.Equal(t, m.Path, core)
	after, err := history.HashTree(root)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestPackageSelectionRejectsBrokenClosure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"missing pair", func(t *testing.T, root string) { require.NoError(t, os.Remove(filepath.Join(root, "adoption.json"))) }},
		{"extra domain bytes", func(t *testing.T, root string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "record", "run", "extra"), nil, 0o600))
		}},
		{"missing selected execution", func(t *testing.T, root string) {
			require.NoError(t, os.RemoveAll(filepath.Join(root, "executions", "replay")))
		}},
		{"tampered declaration", func(t *testing.T, root string) {
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "executions", "capture", "execution.json"), tool.Object{"executionId": "other"}))
		}},
		{"tampered review", func(t *testing.T, root string) {
			var review PackageSelection
			require.NoError(t, tool.ReadJSON(filepath.Join(root, "review.json"), &review))
			review.Executions = review.Executions[:1]
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "review.json"), review))
		}},
		{"inventory duplication", func(t *testing.T, root string) {
			var adoption tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(root, "adoption.json"), &adoption))
			adoption["files"] = tool.Object{}
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "adoption.json"), adoption))
		}},
		{"unknown version", func(t *testing.T, root string) {
			var adoption tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(root, "adoption.json"), &adoption))
			adoption["schemaVersion"] = 3
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "adoption.json"), adoption))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, m := sealedPackage(t)
			tc.mutate(t, root)
			require.Error(t, ValidatePackage(root, m))
		})
	}
}
