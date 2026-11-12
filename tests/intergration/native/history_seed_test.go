package native_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
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
			require.Equal(t, "history/seed", seed["sourceRootRef"])
			for _, name := range []string{"plan.json", "manifest.json", "report.json", "raw/raw.jsonl"} {
				require.Contains(t, tool.Obj(seed["sourceFiles"]), name)
			}

			// Native preparation owns writable startup files. A separately frozen
			// source closure keeps its original bytes after resume appends metadata.
			material := t.TempDir()
			frozenRoot := filepath.Join(material, "history", "seed")
			require.NoError(t, tool.CopyTree(source, frozenRoot))
			require.NoError(t, corpus.VerifyMaterials(frozenRoot, true))
			bindings := map[string]string{}
			for name, digest := range tool.Obj(seed["sourceFiles"]) {
				bindings["history/seed/"+name] = tool.Str(digest)
				require.NoError(t, corpus.VerifyFile(material, "history/seed/"+name, tool.Str(digest)))
			}
			for name, value := range tool.Obj(seed["files"]) {
				require.Equal(t, "history/seed/"+name, tool.Obj(value)["source"])
				writable := filepath.Join(root, name)
				require.NoError(t, corpus.VerifyFile(root, name, tool.Str(tool.Obj(value)["sha256"])))
				f, e := os.OpenFile(writable, os.O_APPEND|os.O_WRONLY, 0o600)
				require.NoError(t, e)
				_, e = f.WriteString("{\"type\":\"system\",\"subtype\":\"mode\"}\n")
				require.NoError(t, e)
				require.NoError(t, f.Close())
				require.NoError(t, corpus.VerifyFile(material, tool.Str(tool.Obj(value)["source"]), tool.Str(tool.Obj(value)["sha256"])))
			}
			require.NoError(t, os.RemoveAll(source))
			for name, digest := range bindings {
				require.NoError(t, corpus.VerifyFile(material, name, digest))
			}
			require.NoError(t, corpus.VerifyMaterials(frozenRoot, true))
			require.NoError(t, os.WriteFile(filepath.Join(frozenRoot, "raw", "raw.jsonl"), []byte("{}\n{}\n"), 0o600))
			require.ErrorContains(t, corpus.VerifyFile(material, "history/seed/raw/raw.jsonl", bindings["history/seed/raw/raw.jsonl"]), "source hash mismatch")

		})
	}
}
