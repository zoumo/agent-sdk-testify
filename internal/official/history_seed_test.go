package official

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHistorySeedUsesCaptureWithoutReplay(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			source := t.TempDir()
			source, err := filepath.EvalSymlinks(source)
			require.NoError(t, err)
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(source, "project"), 0700))
			require.NoError(t, writeFiles(filepath.Join(source, "project"), object{"fixture.txt": "synthetic source\n"}))
			require.NoError(t, os.MkdirAll(filepath.Join(source, "raw"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(source, "raw/raw.jsonl"), []byte("{}\n"), 0600))
			require.NoError(t, writeJSON(filepath.Join(source, "raw/transport.json"), object{"captureStatus": "complete", "cleanupComplete": true}))
			plan := object{"provider": provider, "driverProfile": "history", "project": filepath.Join(source, "project"), "model": "synthetic-model", "recordingIsolation": isolatedRecording, "scenario": object{"parameters": object{"historyTarget": "child-messages"}}}
			require.NoError(t, writeJSON(filepath.Join(source, "plan.json"), plan))
			receipts := []object{{"list_subagents": object{"sessionId": "parent", "agentIds": []string{"child"}}}}
			if provider == "codex" {
				receipts = []object{{"history-query": object{"method": "thread/list", "params": object{"parentThreadId": "parent"}, "response": object{"data": []object{{"id": "child", "parentThreadId": "parent"}}}}}}
			}
			require.NoError(t, writeJSON(filepath.Join(source, "report.json"), object{"status": "passed", "receipts": receipts}))
			saved := object{}
			paths := map[string]string{"config/projects/key/parent.jsonl": "{}\n", "config/projects/key/parent/subagents/agent-child.jsonl": "{}\n"}
			if provider == "codex" {
				paths = map[string]string{"config/sessions/parent.jsonl": "{\"type\":\"session_meta\",\"payload\":{\"id\":\"parent\"}}\n", "config/sessions/child.jsonl": "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"}
			}
			for name, content := range paths {
				require.NoError(t, writeFiles(source, object{name: content}))
				digest, e := fileDigest(filepath.Join(source, name))
				require.NoError(t, e)
				saved[name] = digest
			}
			planDigest, err := fileDigest(filepath.Join(source, "plan.json"))
			require.NoError(t, err)
			require.NoError(t, writeJSON(filepath.Join(source, "manifest.json"), object{"savedFiles": saved, "planSHA256": planDigest, "provenance": "capture", "status": "passed", "captureStatus": "complete"}))
			target := object{"provider": provider}
			require.NoError(t, prepareHistorySeed(source, root, target))
			seed := obj(target["historySeed"])
			require.Equal(t, "parent", seed["sessionId"])
			require.Equal(t, []string{"child"}, seed["agentIds"])
			require.Equal(t, filepath.Join(source, "project"), target["historyProject"])
			require.Len(t, obj(seed["files"]), 2)
			require.NotContains(t, obj(seed["sourceFiles"]), "replay-validation.json")
			require.NoFileExists(t, filepath.Join(source, "replay-validation.json"))
			require.Equal(t, "synthetic-model", target["model"])
			for name, value := range obj(seed["files"]) {
				require.FileExists(t, filepath.Join(root, name))
				require.NoError(t, verifyFile(root, name, str(obj(value)["sha256"])))
				// Native resume may append mode metadata to its writable copy.
				writable := filepath.Join(root, str(obj(value)["source"]))
				f, e := os.OpenFile(writable, os.O_APPEND|os.O_WRONLY, 0600)
				require.NoError(t, e)
				_, e = f.WriteString("{\"type\":\"system\",\"subtype\":\"mode\"}\n")
				require.NoError(t, e)
				require.NoError(t, f.Close())
				require.NoError(t, verifyFile(root, name, str(obj(value)["sha256"])))
			}
		})
	}
}
