package conformance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackageLifecycleInstalledCLI(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "package-lifecycle")
	fixture := prepareFixture(t, env, "--plan", plan)
	require.EqualValues(t, 7, readJSON(t, filepath.Join(fixture, "fixture.json"))["schemaVersion"])
	result, notification := runAgentResult(t, env, fixture, requestOne, "record",
		"--cmd", agent, "--", "app-server", "--listen", "stdio://")
	require.Zero(t, result.code, result.stderr)
	require.EqualValues(t, 3, notification["schemaVersion"])
	require.Equal(t, true, notification["publicationCommitted"])
	materialID := str(notification["materialId"])
	packageRoot := filepath.Join(fixture, "packages", materialID)
	executionRoot := filepath.Join(packageRoot, "executions", str(notification["executionId"]))
	require.DirExists(t, filepath.Join(packageRoot, "material"))
	require.NoDirExists(t, filepath.Join(fixture, "materials"))
	require.NoDirExists(t, filepath.Join(fixture, "executions", str(notification["executionId"])))
	require.NoDirExists(t, filepath.Join(executionRoot, "capture"))
	aggregate := readJSON(t, filepath.Join(executionRoot, "result.json"))
	require.EqualValues(t, 1, aggregate["schemaVersion"])
	require.Equal(t, materialID, aggregate["materialId"])
	assertFileHashes(t, packageRoot, obj(aggregate["files"]))
	recordDescriptor := readJSON(t, filepath.Join(executionRoot, "execution.json"))
	require.Empty(t, obj(recordDescriptor["artifact"])["manifestSHA256"])
	require.Equal(t, materialID, obj(recordDescriptor["artifact"])["id"])
	require.Equal(t, readFile(t, plan), readFile(t, filepath.Join(fixture, "source", "plan.json")))
	current := readJSON(t, filepath.Join(fixture, "current.json"))
	require.Equal(t, materialID, current["materialId"])
	require.Equal(t, digestFile(t, filepath.Join(packageRoot, "material", "material.json")), current["materialSHA256"])
	canonicalFixture, err := filepath.EvalSymlinks(fixture)
	require.NoError(t, err)
	for _, action := range []string{"record", "mock"} {
		snapshot := readJSON(t, filepath.Join(fixture, "runtime", "routes", action+".json"))
		require.EqualValues(t, 1, snapshot["schemaVersion"])
		require.Equal(t, action, snapshot["action"])
		require.Equal(t, materialID, snapshot["materialId"])
		require.Equal(t, fixtureRouteWorkspace(canonicalFixture, action), snapshot["workspace"])
	}
	replay, mock := runAgentResult(t, env, fixture, requestOne, "mock",
		"--", "app-server", "--listen", "stdio://")
	require.Zero(t, replay.code, replay.stderr)
	require.JSONEq(t, result.stdout, replay.stdout)
	require.Equal(t, materialID, mock["materialId"])
	require.Equal(t, false, mock["publicationCommitted"])
	receipt := readJSON(t, resolveFixtureReference(t, packageRoot, str(mock["receipt"])))
	require.EqualValues(t, 2, receipt["schemaVersion"])
	require.EqualValues(t, 1, receipt["version"])
	require.Equal(t, "material", receipt["recording"])
	require.Equal(t, materialID, receipt["materialId"])
	mockAggregate := readJSON(t, filepath.Join(packageRoot, "executions", str(mock["executionId"]), "result.json"))
	assertFileHashes(t, packageRoot, obj(mockAggregate["files"]))
	exported := filepath.Join(root, "exported")
	mustRun(t, env, root, "", "export", "--fixture-dir", fixture, "--output", exported)
	require.Equal(t, treeDigestForPackageTest(t, filepath.Join(packageRoot, "material")), treeDigestForPackageTest(t, exported))
	require.NoError(t, os.RemoveAll(filepath.Join(fixture, "runtime")))
	require.Equal(t, current, readJSON(t, filepath.Join(fixture, "current.json")))
}

func treeDigestForPackageTest(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			result[filepath.ToSlash(relative)] = digestFile(t, path)
		}
		return nil
	}))
	return result
}
