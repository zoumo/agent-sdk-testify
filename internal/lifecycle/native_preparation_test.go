package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestNativePreparationBindsActualEnvironmentAndPlan(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "record-plan.json")
	plan := tool.Object{"recordingIsolation": "personal-skills-v1", "cliSHA256": "native", "args": []string{"app-server"}}
	require.NoError(t, tool.WriteJSON(path, plan))
	execution := Execution{ExecutionID: "execution", Config: filepath.Join(root, "config"), Workspace: filepath.Join(root, "workspace"), RuntimeEnv: map[string]string{"HOME": filepath.Join(root, "home"), "CODEX_HOME": filepath.Join(root, "config"), "OPENAI_API_KEY": "private-credential"}}
	require.NoError(t, os.MkdirAll(filepath.Join(ExecutionDir(root, "execution"), "receipts"), 0o700))
	session := &Session{SessionID: "session", ToolSHA256: "tool"}
	require.NoError(t, SaveNativePreparation(root, session, execution, plan, path))
	var receipt tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(root, "execution"), "receipts", "isolation.json"), &receipt))
	require.Equal(t, execution.RuntimeEnv["HOME"], tool.Obj(receipt["environment"])["HOME"])
	require.NotContains(t, tool.Obj(receipt["environment"]), "OPENAI_API_KEY")
	digest, err := tool.FileDigest(path)
	require.NoError(t, err)
	require.Equal(t, digest, receipt["preparedPlanSHA256"])
	require.Equal(t, "execution", receipt["executionId"])
}
