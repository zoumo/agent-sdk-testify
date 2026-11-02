package history

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeHistoryDerivationRejectsCorruptionAndPreservesOtherBytes(t *testing.T) {
	for _, tc := range []struct {
		name, filename, source string
		fail                   bool
	}{
		{"lexemes", "record.jsonl", `{"cwd":"/original/project","future":1.2500,"id":"keep","nested":{"cwd":"do-not-edit"}}` + "\n", false},
		{"pretty-sidecar", "agent.meta.json", "{\n  \"cwd\": \"/original/project\",\n  \"future\": 9007199254740993\n}\n", false},
		{"duplicate", "record.jsonl", `{"cwd":"/original/project","cwd":"/other"}` + "\n", true},
		{"foreign", "record.jsonl", `{"cwd":"/other/project"}` + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, root, workspace := t.TempDir(), t.TempDir(), t.TempDir()
			path := filepath.Join(source, "config", "projects", "-original-project", tc.filename)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(tc.source), 0o600))
			err := MaterializeClaude(source, root, workspace, object{"project": "/original/project"})
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(workspace, "-")
				derived, err := os.ReadFile(filepath.Join(root, "config", "projects", key, tc.filename))
				require.NoError(t, err)
				require.Equal(t, bytes.ReplaceAll([]byte(tc.source), []byte("/original/project"), []byte(workspace)), derived)
			}
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.source, string(original))
		})
	}
}
