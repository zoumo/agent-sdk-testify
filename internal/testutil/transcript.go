package testutil

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// BuiltinTranscript reads immutable recorded inputs for transport and build checks.
func BuiltinTranscript(t *testing.T, provider, id, connection string) (string, string) {
	t.Helper()
	repository, err := RepositoryRoot()
	require.NoError(t, err)
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(repository, "data", "index.json"), &index))
	for _, value := range tool.Array(index["cases"]) {
		row := tool.Obj(value)
		if row["provider"] != provider || row["caseId"] != id {
			continue
		}
		root := filepath.Join(repository, "data", tool.Str(row["materials"]))
		rawRoot := filepath.Join(root, "raw")
		if connection == "probe" {
			rawRoot = filepath.Join(root, "remote-probe", "raw")
		} else if connection != "main" {
			t.Fatalf("unknown builtin connection %q", connection)
		}
		data, err := os.ReadFile(filepath.Join(rawRoot, "raw.jsonl"))
		require.NoError(t, err)
		var input strings.Builder
		for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
			var frame map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(line, &frame))
			if string(frame["direction"]) == `"c2s"` {
				input.Write(frame["wire"])
				input.WriteByte(10)
			}
		}
		return input.String(), root
	}
	t.Fatal("missing test capture")
	return "", ""
}
