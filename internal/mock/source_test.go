package mock

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func mockCapture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "raw"), 0o700))
	plan := []byte(`{"provider":"codex","provenance":"synthetic"}` + "\n")
	raw := []byte("{\"ordinal\":1,\"direction\":\"c2s\",\"channel\":\"protocol\",\"epoch\":1,\"wire\":{\"id\":1,\"method\":\"ping\",\"params\":{\"unknown\":9007199254740993}}}\n" +
		"{\"ordinal\":2,\"direction\":\"s2c\",\"channel\":\"protocol\",\"epoch\":1,\"wire\":{\"id\":1,\"result\":{\"unknown\":9007199254740993}}}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), plan, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), raw, 0o600))
	digest := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{
		"provenance": "synthetic", "captureStatus": "complete", "planSHA256": digest(plan),
		"files": tool.Object{"raw.jsonl": digest(raw)},
	}))
	return root
}

func mockSourceBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	}))
	return files
}

func TestMockPhaseRejectsInvalidSources(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		change           func(*testing.T, string)
	}{
		{"plan drift", "source hash mismatch: plan.json", func(t *testing.T, root string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), []byte(`{"provider":"claude"}`), 0o600))
		}},
		{"raw drift", "source hash mismatch: raw.jsonl", func(t *testing.T, root string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte("{}\n"), 0o600))
		}},
		{"missing raw", "raw.jsonl", func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "raw", "raw.jsonl")))
		}},
		{"missing manifest", "manifest.json", func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "manifest.json")))
		}},
		{"missing source bindings", "planSHA256", func(t *testing.T, root string) {
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{"provenance": "synthetic"}))
		}},
		{"incomplete capture", "source recording capture is partial", func(t *testing.T, root string) {
			var m tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(root, "manifest.json"), &m))
			m["captureStatus"] = "partial"
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), m))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := mockCapture(t)
			if tc.change != nil {
				tc.change(t, root)
			}
			before := mockSourceBytes(t, root)
			_, _, err := loadMockPhase(root, "codex", "main")
			require.ErrorContains(t, err, tc.diagnostic)
			assert.Equal(t, before, mockSourceBytes(t, root))
		})
	}
}

func TestMockRejectsUnsupportedSourceFormat(t *testing.T) {
	root := mockCapture(t)
	raw, err := os.ReadFile(filepath.Join(root, "raw", "raw.jsonl"))
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "raw", "manifest.json"), tool.Object{
		"recordingFormat": tool.Object{"name": "agent-mock-jsonl", "version": 999, "transport": "stdio-jsonl"},
		"files":           tool.Object{"raw.jsonl": hex.EncodeToString(sum[:])},
	}))
	_, _, err = loadMockPhase(root, "codex", "main")
	require.ErrorContains(t, err, "unsupported recording format")
}
