package official

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestDriverRuntimeSeparatesAttemptLocations(t *testing.T) {
	root := t.TempDir()
	runtime := newDriverRuntime(
		filepath.Join(root, "driver"),
		filepath.Join(root, "workspace"),
		filepath.Join(root, "home"),
		filepath.Join(root, "config"),
		filepath.Join(root, "raw"),
		filepath.Join(root, "tmp"),
		filepath.Join(root, "seed"),
		true,
	)
	require.NoError(t, validateDriverRuntime(runtime))
	require.Equal(t, float64(1), runtime["schemaVersion"])
	require.Equal(t, filepath.Join(root, "driver"), runtime["output"])
	require.Equal(t, filepath.Join(root, "seed"), runtime["seedRoot"])
	for _, key := range []string{"caseId", "project", "historyProject", "args", "cliSHA256", "historySeed"} {
		require.NotContains(t, runtime, key)
	}
}

func TestDriverRuntimeRejectsUnknownOrIncompleteConfiguration(t *testing.T) {
	root := t.TempDir()
	valid := func() tool.Object {
		return newDriverRuntime(
			filepath.Join(root, "driver"),
			filepath.Join(root, "workspace"),
			filepath.Join(root, "home"),
			filepath.Join(root, "config"),
			filepath.Join(root, "raw"),
			filepath.Join(root, "tmp"),
			"",
			false,
		)
	}
	for _, tc := range []struct {
		name   string
		mutate func(tool.Object)
	}{
		{"unknown version", func(runtime tool.Object) { runtime["schemaVersion"] = float64(2) }},
		{"missing version", func(runtime tool.Object) { delete(runtime, "schemaVersion") }},
		{"missing root", func(runtime tool.Object) { delete(runtime, "root") }},
		{"relative workspace", func(runtime tool.Object) { runtime["workspace"] = "workspace" }},
		{"missing replay", func(runtime tool.Object) { delete(runtime, "replay") }},
		{"non boolean replay", func(runtime tool.Object) { runtime["replay"] = "true" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := valid()
			tc.mutate(runtime)
			require.Error(t, validateDriverRuntime(runtime))
		})
	}
	require.NoError(t, validateDriverRuntime(valid()))
}

func TestRunDriverRejectsRuntimeBeforeCreatingAttemptRoot(t *testing.T) {
	root := t.TempDir()
	attempt := filepath.Join(root, "attempt")
	runtime := newDriverRuntime(
		attempt,
		filepath.Join(root, "workspace"),
		filepath.Join(root, "home"),
		filepath.Join(root, "config"),
		filepath.Join(root, "raw"),
		filepath.Join(root, "tmp"),
		"",
		false,
	)
	runtime["schemaVersion"] = float64(99)
	_, err := runDriver(context.Background(), driverRunOptions{
		Root:     attempt,
		Material: tool.Object{"caseId": "SDK-CASE-001"},
		Runtime:  runtime,
	})
	require.ErrorContains(t, err, "runtime configuration schema 1")
	_, statErr := os.Stat(attempt)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}
