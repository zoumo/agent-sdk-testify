package official

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestSelectedCodexProviderAuth(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "config"), 0o700))
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "config.toml"), []byte(`model_provider = "selected"
[model_providers.selected]
env_key = "SDK_TEST_SELECTED_TOKEN"
env_http_headers = { "X-Account" = "SDK_TEST_ACCOUNT" }
[model_providers.other]
env_key = "SDK_TEST_OTHER_TOKEN"
`), 0o600))
	t.Setenv("SDK_TEST_SELECTED_TOKEN", "synthetic-token")
	t.Setenv("SDK_TEST_ACCOUNT", "synthetic-account")
	t.Setenv("SDK_TEST_OTHER_TOKEN", "other-token")
	self, err := os.Executable()
	require.NoError(t, err)
	plan := tool.Object{"provider": "codex", "project": root, "scenario": tool.Object{}}
	require.NoError(t, prepareNative(context.Background(), &Options{CLI: self, Config: source}, plan, root))
	var env map[string]string
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "native.env.private.json"), &env))
	require.Equal(t, "synthetic-token", env["SDK_TEST_SELECTED_TOKEN"])
	require.Equal(t, "synthetic-account", env["SDK_TEST_ACCOUNT"])
	require.NotContains(t, env, "SDK_TEST_OTHER_TOKEN")
}

func TestVersionProbeHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "hanging-cli")
	require.NoError(t, os.WriteFile(cli, []byte("#!/bin/sh\nsleep 60\n"), 0o700))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := prepareNative(ctx, &Options{CLI: cli}, tool.Object{"provider": "codex"}, root)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 3*time.Second)
}

func TestChangedRecordingSourceIsRejected(t *testing.T) {
	o := sample(t, "passed")
	source := filepath.Join(o.Materials, "raw", "raw.jsonl")
	data, err := os.ReadFile(source)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	require.NoError(t, tool.WriteJSON(filepath.Join(o.Materials, "manifest.json"), tool.Object{"files": tool.Object{"raw.jsonl": hex.EncodeToString(sum[:])}}))
	_, err = loadMaterials(o)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source, append(data, '\n'), 0o600))
	_, err = loadMaterials(o)
	require.ErrorContains(t, err, "source hash mismatch")
}

func TestIncompleteSourceCaptureCannotPassReplay(t *testing.T) {
	for _, transport := range []tool.Object{{"captureStatus": "failed", "cleanupComplete": true}, {"captureStatus": "complete", "cleanupComplete": false}} {
		t.Run(tool.Str(transport["captureStatus"]), func(t *testing.T) {
			o := sample(t, "passed")
			require.NoError(t, tool.WriteJSON(filepath.Join(o.Materials, "raw", "transport.json"), transport))
			_, err := loadMaterials(o)
			require.ErrorContains(t, err, "incomplete")
		})
	}
}

func TestIncompleteRemoteCaptureCannotPassReplay(t *testing.T) {
	for _, phase := range []string{"probe", "service"} {
		t.Run(phase, func(t *testing.T) {
			o := sample(t, "passed")
			complete := tool.Object{"captureStatus": "complete", "cleanupComplete": true}
			require.NoError(t, tool.WriteJSON(filepath.Join(o.Materials, "raw", "transport.json"), complete))
			require.NoError(t, os.MkdirAll(filepath.Join(o.Materials, "remote-probe", "raw"), 0o700))
			probe := complete
			if phase == "probe" {
				probe = tool.Object{"captureStatus": "failed", "cleanupComplete": true}
			}
			require.NoError(t, tool.WriteJSON(filepath.Join(o.Materials, "remote-probe", "raw", "transport.json"), probe))
			if phase == "service" {
				require.NoError(t, tool.WriteJSON(filepath.Join(o.Materials, "remote-service.json"), tool.Object{"serverExitCode": 0, "cleanupComplete": false}))
			}
			_, err := loadMaterials(o)
			require.ErrorContains(t, err, "incomplete")
		})
	}
}

func TestSelectedConfigExcludesIntegrationsAndKeepsEndpointCredentialsTogether(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "config"), 0o700))
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "config.toml"), []byte(`model="synthetic"
model_provider="selected"
notify=["must-not-run"]
[projects.private]
trust_level="trusted"
[mcp_servers.private]
command="must-not-run"
[model_providers.selected]
base_url="synthetic-endpoint"
env_key="SYNTHETIC_CREDENTIAL"
[model_providers.other]
env_key="OTHER_CREDENTIAL"
`), 0o600))
	self, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, prepareNative(context.Background(), &Options{CLI: self, Config: source}, tool.Object{"provider": "codex", "project": root, "scenario": tool.Object{}}, root))
	config, err := os.ReadFile(filepath.Join(root, "config", "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(config), "synthetic-endpoint")
	for _, excluded := range []string{"notify", "projects", "mcp_servers", "OTHER_CREDENTIAL"} {
		require.NotContains(t, string(config), excluded)
	}
	env := map[string]string{"ANTHROPIC_BASE_URL": "other-endpoint", "ANTHROPIC_AUTH_TOKEN": "other-token"}
	settings := filepath.Join(root, "settings.json")
	require.NoError(t, tool.WriteJSON(settings, tool.Object{"env": tool.Object{"ANTHROPIC_BASE_URL": "selected-endpoint", "ANTHROPIC_API_KEY": "selected-key"}}))
	claudeAuthEnvironment(settings, env)
	require.Equal(t, map[string]string{"ANTHROPIC_BASE_URL": "selected-endpoint", "ANTHROPIC_API_KEY": "selected-key"}, env)
}

func TestCaptureScansConfiguredCredentialsWithoutKeySuffix(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "config"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte("model_provider='selected'\n[model_providers.selected]\nenv_key='SYNTHETIC_CREDENTIAL'\n"), 0o600))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "native.env.private.json"), map[string]string{"SYNTHETIC_CREDENTIAL": "synthetic-scan-canary"}))
	raw := filepath.Join(root, "raw", "raw.jsonl")
	require.NoError(t, os.WriteFile(raw, []byte("original synthetic-scan-canary bytes\n"), 0o600))
	status, err := corpus.ScanCaptureCredentials(root)
	require.Equal(t, "quarantined", status)
	require.Error(t, err)
	unchanged, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, "original synthetic-scan-canary bytes\n", string(unchanged))
	require.NoError(t, os.WriteFile(raw, []byte("safe synthetic data\n"), 0o600))
	status, err = corpus.ScanCaptureCredentials(root)
	require.NoError(t, err)
	require.Equal(t, "pass", status)
}

func TestLegacyDriverSnapshotHashAndRegularSourceRemainRequired(t *testing.T) {
	o := sample(t, "passed")
	path := filepath.Join(o.Materials, "driver", "source.py")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	original := []byte("# original saved driver source\n")
	require.NoError(t, os.WriteFile(path, original, 0o600))
	sum := sha256.Sum256(original)
	require.NoError(t, tool.WriteJSON(filepath.Join(o.Materials, "manifest.json"), tool.Object{"driverFiles": tool.Object{"driver/source.py": hex.EncodeToString(sum[:])}}))
	_, err := loadMaterials(o)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("changed"), 0o600))
	_, err = loadMaterials(o)
	require.ErrorContains(t, err, "source hash mismatch")
	saved := filepath.Join(t.TempDir(), "saved.py")
	require.NoError(t, os.WriteFile(saved, original, 0o600))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(saved, path))
	_, err = loadMaterials(o)
	require.ErrorContains(t, err, "not a regular file")
}

func TestClaudeConfigCopyRetainsNativeDefaultModelOnly(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "config"), 0o700))
	settings := tool.Object{"model": "synthetic-default", "hooks": tool.Object{"must-not-run": true}, "env": tool.Object{"ANTHROPIC_BASE_URL": "selected-endpoint", "ANTHROPIC_API_KEY": "selected-key"}}
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "settings.json"), settings))
	originalBytes, err := os.ReadFile(filepath.Join(source, "settings.json"))
	require.NoError(t, err)
	self, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, prepareNative(context.Background(), &Options{CLI: self, Config: source}, tool.Object{"provider": "claude", "project": root, "scenario": tool.Object{}}, root))
	var copied, original tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "config", "settings.json"), &copied))
	require.Equal(t, tool.Object{"model": "synthetic-default"}, copied)
	require.NoError(t, tool.ReadJSON(filepath.Join(source, "settings.json"), &original))
	require.Equal(t, settings, original)
	unchanged, err := os.ReadFile(filepath.Join(source, "settings.json"))
	require.NoError(t, err)
	require.Equal(t, originalBytes, unchanged)
}

func TestClaudeInitialModelIsDistinctFromChangeTarget(t *testing.T) {
	plan := tool.Object{"model": "target", "initialModel": "initial"}
	args, err := prepareClaudeArgs(plan, nil, "structured-input")
	require.NoError(t, err)
	require.Equal(t, []string{"--model", "initial"}, args[len(args)-2:])
	require.Equal(t, "target", plan["model"])
	delete(plan, "initialModel")
	args, err = prepareClaudeArgs(plan, nil, "structured-input")
	require.NoError(t, err)
	require.Equal(t, []string{"--model", "target"}, args[len(args)-2:])
}

func TestInitialModelRecordingScope(t *testing.T) {
	cli, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, initial, target string
		parameters            tool.Object
		wantError             string
	}{
		{"same model", "a", "a", tool.Object{}, "different explicit target"},
		{"missing target", "a", "", tool.Object{}, "different explicit target"},
		{"not model change", "a", "b", tool.Object{"setting": "effort"}, "only valid for model-change"},
		{"creation", "a", "b", tool.Object{"setting": "model", "configurationTiming": "creation"}, "only valid for model-change"},
		{"change", "a", "b", tool.Object{"setting": "model", "configurationTiming": "between-inputs"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			plan := tool.Object{"provider": "claude", "project": root, "scenario": tool.Object{"parameters": tc.parameters}}
			err := prepareNative(context.Background(), &Options{CLI: cli, Model: tc.target, InitialModel: tc.initial}, plan, root)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.initial, plan["initialModel"])
			require.Equal(t, tc.target, plan["model"])
		})
	}
}
