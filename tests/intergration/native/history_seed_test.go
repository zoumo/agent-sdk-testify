package native_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	nativeprep "github.com/zoumo/agent-sdk-testify/internal/native"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestHistorySeedUsesCaptureWithoutReplay(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			source := t.TempDir()
			source, err := filepath.EvalSymlinks(source)
			require.NoError(t, err)
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(source, "project"), 0o700))
			require.NoError(t, tool.WriteFiles(filepath.Join(source, "project"), tool.Object{"fixture.txt": "synthetic source\n"}))
			require.NoError(t, os.MkdirAll(filepath.Join(source, "raw"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(source, "raw", "raw.jsonl"), []byte("{}\n"), 0o600))
			require.NoError(t, tool.WriteJSON(filepath.Join(source, "raw", "transport.json"), tool.Object{"captureStatus": "complete", "cleanupComplete": true}))
			plan := tool.Object{"provider": provider, "driverProfile": "history", "project": filepath.Join(source, "project"), "model": "synthetic-model", "recordingIsolation": "personal-skills-v1", "scenario": tool.Object{"parameters": tool.Object{"historyTarget": "child-messages"}, "files": tool.Object{"fixture.txt": "synthetic source\n"}}}
			require.NoError(t, tool.WriteJSON(filepath.Join(source, "plan.json"), plan))
			receipts := []tool.Object{{"list_subagents": tool.Object{"sessionId": "parent", "agentIds": []string{"child"}}}}
			if provider == "codex" {
				receipts = []tool.Object{{"history-query": tool.Object{"method": "thread/list", "params": tool.Object{"parentThreadId": "parent"}, "response": tool.Object{"data": []tool.Object{{"id": "child", "parentThreadId": "parent"}}}}}}
			}
			require.NoError(t, tool.WriteJSON(filepath.Join(source, "report.json"), tool.Object{"status": "passed", "receipts": receipts}))
			saved := tool.Object{}
			paths := map[string]string{"config/projects/key/parent.jsonl": "{}\n", "config/projects/key/parent/subagents/agent-child.jsonl": "{}\n"}
			if provider == "codex" {
				paths = map[string]string{"config/sessions/parent.jsonl": "{\"type\":\"session_meta\",\"payload\":{\"id\":\"parent\"}}\n", "config/sessions/child.jsonl": "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"}
			}
			for name, content := range paths {
				require.NoError(t, tool.WriteFiles(source, tool.Object{name: content}))
				digest, e := tool.FileDigest(filepath.Join(source, name))
				require.NoError(t, e)
				saved[name] = digest
			}
			planDigest, err := tool.FileDigest(filepath.Join(source, "plan.json"))
			require.NoError(t, err)
			require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), tool.Object{"savedFiles": saved, "planSHA256": planDigest, "provenance": "capture", "status": "passed", "captureStatus": "complete"}))
			target := tool.Object{"provider": provider}
			require.NoError(t, nativeprep.PrepareHistorySeed(source, root, target))
			seed := tool.Obj(target["historySeed"])
			require.Equal(t, "parent", seed["sessionId"])
			require.Equal(t, "history-seed/project", seed["projectSnapshot"])
			frozen, err := os.ReadFile(filepath.Join(root, "history-seed", "project", "fixture.txt"))
			require.NoError(t, err)
			require.Equal(t, "synthetic source\n", string(frozen))
			require.Equal(t, []string{"child"}, seed["agentIds"])
			require.Equal(t, filepath.Join(source, "project"), target["historyProject"])
			require.Len(t, tool.Obj(seed["files"]), 2)
			require.NotContains(t, tool.Obj(seed["sourceFiles"]), "replay-validation.json")
			require.NoFileExists(t, filepath.Join(source, "replay-validation.json"))
			require.Equal(t, "synthetic-model", target["model"])
			require.NotContains(t, seed, "sourceRoot")
			require.Equal(t, "history-seed/source", seed["sourceRootRef"])
			for _, name := range []string{"plan.json", "manifest.json", "report.json", "raw/raw.jsonl"} {
				require.Contains(t, tool.Obj(seed["sourceFiles"]), name)
			}
			require.NoError(t, corpus.VerifyMaterials(filepath.Join(root, "history-seed", "source"), true))
			for name, value := range tool.Obj(seed["files"]) {
				require.FileExists(t, filepath.Join(root, name))
				require.NoError(t, corpus.VerifyFile(root, name, tool.Str(tool.Obj(value)["sha256"])))
				require.Equal(t, filepath.ToSlash(filepath.Join("history-seed", "source", strings.TrimPrefix(name, "history-seed/"))), tool.Str(tool.Obj(value)["source"]))
				// Native resume may append mode metadata to its writable copy.
				writable := filepath.Join(root, strings.TrimPrefix(name, "history-seed/"))
				f, e := os.OpenFile(writable, os.O_APPEND|os.O_WRONLY, 0o600)
				require.NoError(t, e)
				_, e = f.WriteString("{\"type\":\"system\",\"subtype\":\"mode\"}\n")
				require.NoError(t, e)
				require.NoError(t, f.Close())
				require.NoError(t, corpus.VerifyFile(root, name, tool.Str(tool.Obj(value)["sha256"])))
			}
			// Bindings verify the complete frozen seed closure from the fixture
			// itself; the prepare source is no longer a read location.
			savedFiles := tool.Object{}
			for name := range paths {
				digest, err := tool.FileDigest(filepath.Join(root, name))
				require.NoError(t, err)
				savedFiles[name] = digest
			}
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{"savedFiles": savedFiles}))
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "plan.json"), target))
			bindings, err := history.Bindings(root)
			require.NoError(t, err)
			require.Contains(t, bindings, "history-seed/source/raw/raw.jsonl")
			require.NoError(t, os.RemoveAll(source))
			require.NoError(t, history.Check(root, bindings))
			require.NoError(t, corpus.VerifyMaterials(filepath.Join(root, "history-seed", "source"), true))
			require.NoError(t, os.WriteFile(filepath.Join(root, "history-seed", "source", "raw", "raw.jsonl"), []byte("{}\n{}\n"), 0o600))
			_, err = history.Bindings(root)
			require.ErrorContains(t, err, "history hash mismatch")
		})
	}
}
