package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestNativePreparationBindsActualEnvironmentAndPlan(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "record-plan.json")
	materialPath := filepath.Join(root, "material-plan.json")
	plan := tool.Object{"recordingIsolation": "personal-skills-v1", "cliSHA256": "native", "args": []string{"app-server"}}
	require.NoError(t, tool.WriteJSON(path, plan))
	require.NoError(t, tool.WriteJSON(materialPath, plan))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "executions"), 0o700))
	require.NoError(t, tool.WriteJSON(IndexPath(root), Index{SchemaVersion: SchemaVersion, Executions: []IndexExecution{{ID: "execution", MaterialID: "material"}}}))
	execution := Execution{Artifact: corpus.Material{ID: "material"}, ExecutionID: "execution", Config: filepath.Join(root, "config"), Workspace: filepath.Join(root, "workspace"), RuntimeEnv: map[string]string{"HOME": filepath.Join(root, "home"), "CODEX_HOME": filepath.Join(root, "config"), "OPENAI_API_KEY": "private-credential"}}
	require.NoError(t, os.MkdirAll(filepath.Join(ExecutionDir(root, "execution"), "receipts"), 0o700))
	require.NoError(t, tool.WriteJSON(filepath.Join(ExecutionDir(root, "execution"), "execution.json"), tool.Object{"executionId": "execution"}))
	fixture := &Fixture{FixtureID: "fixture", Tool: ToolIdentity{Source: ToolSourceExecutable, SHA256: "tool"}}
	require.NoError(t, SaveNativePreparation(root, fixture, execution, plan, path, materialPath))
	var receipt tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(root, "execution"), "isolation.json"), &receipt))
	require.EqualValues(t, 3, receipt["schemaVersion"])
	require.Equal(t, execution.RuntimeEnv["HOME"], tool.Obj(receipt["environment"])["HOME"])
	require.NotContains(t, tool.Obj(receipt["environment"]), "OPENAI_API_KEY")
	digest, err := tool.FileDigest(filepath.Join(ExecutionDir(root, "execution"), "prepared-plan.json"))
	require.NoError(t, err)
	require.Equal(t, digest, tool.Obj(receipt["preparedPlan"])["sha256"])
	require.Equal(t, "execution", receipt["executionId"])
}
