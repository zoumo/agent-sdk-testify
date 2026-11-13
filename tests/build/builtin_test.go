package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/testutil/builtintest"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

func TestBuiltinGeneratorArtifactContract(t *testing.T) {
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	generator := filepath.Join(t.TempDir(), "generator")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-tags", "builtin_generate", "-o", generator, "./cmd/agent-sdk-testify")
	cmd.Dir = repository
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	// Check recomputes the entire closure rather than trusting generated hashes.
	cmd = exec.CommandContext(t.Context(), generator, "--check")
	cmd.Dir = repository
	data, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	root := t.TempDir()
	require.NoError(t, tool.CopyTree(filepath.Join(repository, "data"), filepath.Join(root, "data")))
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "data", "builtin", "manifest.json"), &manifest))
	require.NotContains(t, manifest, "toolVersion")
	cmd = exec.CommandContext(t.Context(), generator, "--root", root, "--revision", tool.Str(manifest["sourceRevision"]))
	cmd.Dir = repository
	data, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	for _, name := range []string{"manifest.json", "materials.tar.gz"} {
		expected, readErr := os.ReadFile(filepath.Join(repository, "data", "builtin", name))
		require.NoError(t, readErr)
		actual, readErr := os.ReadFile(filepath.Join(root, "data", "builtin", name))
		require.NoError(t, readErr)
		require.Equal(t, expected, actual, name)
	}
	taggedGenerator := filepath.Join(t.TempDir(), "tagged-generator")
	ldflags := "-X=" + version.Module + "/internal/version.buildVersion=v99.0.0"
	cmd = exec.CommandContext(t.Context(), "go", "build", "-tags", "builtin_generate", "-ldflags", ldflags, "-o", taggedGenerator, "./cmd/agent-sdk-testify")
	cmd.Dir = repository
	data, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	cmd = exec.CommandContext(t.Context(), taggedGenerator, "--root", root, "--check")
	cmd.Dir = repository
	data, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	invoke := func() ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), generator, "--root", root, "--check")
		cmd.Dir = repository
		return cmd.CombinedOutput()
	}
	// A selected missing file must fail, even though the generated bundle exists.
	_, source := builtintest.BuiltinTranscript(t, "codex", "SDK-CASE-001", "main")
	relativeSource, err := filepath.Rel(repository, source)
	require.NoError(t, err)
	raw := filepath.Join(root, relativeSource, "raw", "raw.jsonl")
	original, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.NoError(t, os.Remove(raw))
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "material core inventory is incomplete or contains extra files")
	require.NoError(t, os.WriteFile(raw, append(original, 10), 0o600))
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "material core inventory hash mismatch: raw/raw.jsonl")
	require.NoError(t, os.WriteFile(raw, original, 0o600))
	// Changes in the authoritative contract invalidate the generated snapshot.
	contract := filepath.Join(root, "data", "scenarios", "SDK-US-001.json")
	f, err := os.OpenFile(contract, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "builtin drift")
	indexPath := filepath.Join(root, "data", "index.json")
	var empty tool.Object
	require.NoError(t, tool.ReadJSON(indexPath, &empty))
	for _, value := range tool.Array(empty["cases"]) {
		row := tool.Obj(value)
		row["verification"] = "unverified"
		row["verificationReason"] = "no selected official evidence in this test"
	}
	require.NoError(t, tool.WriteJSON(indexPath, empty))
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "empty qualified builtin set")
}
