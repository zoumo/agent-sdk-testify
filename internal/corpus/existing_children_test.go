package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestChildObservationArgsRequireOwnSetupIdentity(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			parent := "12345678-1234-4234-8234-123456789012"
			plan := tool.Object{
				"provider": provider, "driverProfile": "existing-children",
				"scenario":              tool.Object{"parameters": tool.Object{"selfSeed": true}},
				"args":                  []string{"native-protocol"},
				"existingChildrenSetup": tool.Object{"schemaVersion": float64(1), "provider": provider, "sessionId": parent},
			}
			require.True(t, SelfSeededChildren(plan))
			args, err := ChildObservationArgs(plan)
			require.NoError(t, err)
			expected := []string{"native-protocol"}
			if provider == "claude" {
				expected = append(expected, "--resume", parent)
			}
			require.Equal(t, expected, args)
			require.Equal(t, []string{"native-protocol"}, plan["args"])
			for _, field := range []string{"schemaVersion", "provider", "sessionId"} {
				setup := tool.Obj(plan["existingChildrenSetup"])
				original := setup[field]
				setup[field] = "invalid"
				_, err := ChildObservationArgs(plan)
				require.Error(t, err)
				setup[field] = original
			}
			plan["historySeed"] = tool.Object{"sessionId": "legacy"}
			require.False(t, SelfSeededChildren(plan))
		})
	}
}

func TestChildObservationBindsCompletedSetupAndNativeHistory(t *testing.T) {
	root := t.TempDir()
	parent := "12345678-1234-4234-8234-123456789012"
	child := "87654321-4321-4321-8321-210987654321"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "raw"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "existing-children"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte("synthetic protocol bytes\n"), 0o600))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "raw", "transport.json"), tool.Object{
		"captureStatus": "complete", "nativeExitCode": 0, "nativeLaunched": true,
		"reaped": true, "clientEOF": true, "cleanupComplete": true,
	}))
	bindings := []history.Binding{}
	for _, identity := range []string{parent, child} {
		target := "config/sessions/" + identity + ".jsonl"
		source := "history/seed/" + target
		payload := tool.Object{"id": identity, "cwd": "/synthetic"}
		binding := history.Binding{Role: "seed", SessionID: identity, Project: "/synthetic", NativeTarget: target, Source: source}
		if identity == child {
			payload["parent_thread_id"] = parent
			binding.ParentSessionID, binding.ChildSessionID = parent, child
		}
		path := filepath.Join(root, filepath.FromSlash(source))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		encoded, err := json.Marshal(tool.Object{"type": "session_meta", "payload": payload})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
		digest, err := tool.FileDigest(path)
		require.NoError(t, err)
		binding.SHA256 = digest
		bindings = append(bindings, binding)
	}
	rawHash, err := tool.FileDigest(filepath.Join(root, "raw", "raw.jsonl"))
	require.NoError(t, err)
	transportHash, err := tool.FileDigest(filepath.Join(root, "raw", "transport.json"))
	require.NoError(t, err)
	setupPath := filepath.Join(root, "existing-children", "setup.json")
	require.NoError(t, tool.WriteJSON(setupPath, tool.Object{
		"schemaVersion": 1, "provider": "codex", "sessionId": parent, "agentIds": []string{child},
		"historyBindings": bindings, "rawSHA256": rawHash, "transportSHA256": transportHash,
		"completedAt": "2026-11-13T00:00:00Z",
	}))
	var setup tool.Object
	require.NoError(t, tool.ReadJSON(setupPath, &setup))
	plan := tool.Object{"provider": "codex", "existingChildrenSetup": setup}
	setupHash, err := tool.FileDigest(setupPath)
	require.NoError(t, err)
	marker := tool.Object{"schemaVersion": 1, "sessionId": parent, "setupSHA256": setupHash, "startedAt": "2026-11-13T00:00:01Z"}
	markerPath := filepath.Join(root, "existing-children", "observation.json")
	require.Error(t, validateChildObservation(root, plan))
	require.NoError(t, tool.WriteJSON(markerPath, marker))
	require.NoError(t, validateChildObservation(root, plan))
	for _, variant := range []struct{ field, value string }{
		{"sessionId", child},
		{"setupSHA256", rawHash},
		{"startedAt", "2026-11-13T00:00:00Z"},
		{"startedAt", "2026-11-12T23:59:59Z"},
	} {
		t.Run(variant.field+"/"+variant.value, func(t *testing.T) {
			original := marker[variant.field]
			marker[variant.field] = variant.value
			require.NoError(t, tool.WriteJSON(markerPath, marker))
			require.Error(t, validateChildObservation(root, plan))
			marker[variant.field] = original
		})
	}
	require.NoError(t, tool.WriteJSON(markerPath, marker))
	setup["sessionId"] = child
	require.Error(t, validateChildObservation(root, plan))
	setup["sessionId"] = parent
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(bindings[1].Source)), []byte("changed native history\n"), 0o600))
	require.Error(t, validateChildObservation(root, plan))
}
