package native

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestCanonicalSeedRequiresPackageBusinessEvidence(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		p := filepath.Join(source, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, data, 0o600))
	}
	bindings := []history.Binding{}
	nativeFiles := map[string][]byte{}
	for _, id := range []string{"parent", "child"} {
		payload := tool.Object{"id": id, "cwd": "/old/project"}
		parent := ""
		if id == "child" {
			parent = "parent"
			payload["parentThreadId"] = parent
		}
		data, err := json.Marshal(tool.Object{"type": "session_meta", "payload": payload})
		require.NoError(t, err)
		data = append(data, '\n')
		name := "config/sessions/" + id + ".jsonl"
		nativeFiles[name] = data
		path := "history/runtime/" + name
		write(path, data)
		binding := history.Binding{Role: "runtime", SessionID: id, ParentSessionID: parent, Project: "/old/project", NativeTarget: name, Source: path, SHA256: tool.DigestBytes(data)}
		if parent != "" {
			binding.ChildSessionID = id
		}
		bindings = append(bindings, binding)
	}
	original, err := json.Marshal(tool.Object{
		"provider": "codex", "project": "/old/project", "driverProfile": "history", "recordingIsolation": isolatedRecording,
		"scenario": tool.Object{"parameters": tool.Object{"historyTarget": "child-messages"}, "files": tool.Object{"fixture.txt": "original\n"}},
	})
	require.NoError(t, err)
	write("source-plan.json", original)
	plan, err := corpus.MaterialPlanBytes(original, tool.Object{"sourcePlanSHA256": tool.DigestBytes(original), "historyBindings": bindings})
	require.NoError(t, err)
	write("plan.json", plan)
	write("raw/raw.jsonl", nil)
	write("raw/stderr.raw", nil)
	write("raw/manifest.json", []byte("{}\n"))
	write("raw/transport.json", []byte("{\"captureStatus\":\"complete\",\"cleanupComplete\":true}\n"))
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "report.json"), tool.Object{
		"status": "passed", "receipts": []any{tool.Object{"history-query": tool.Object{
			"method": "thread/list", "params": tool.Object{"parentThreadId": "parent"},
			"response": tool.Object{"data": []any{tool.Object{"id": "child", "parentThreadId": "parent"}}},
		}}},
	}))
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), tool.Object{
		"provenance": "capture", "status": "captured", "captureStatus": "complete",
		"planSHA256": tool.DigestBytes(plan), "sourcePlanSHA256": tool.DigestBytes(original),
	}))
	require.NoError(t, corpus.SealCoreInventory(source))
	manifestHash, err := tool.FileDigest(filepath.Join(source, "manifest.json"))
	require.NoError(t, err)
	material := corpus.Material{FormatVersion: 3, ID: "synthetic-seed", Kind: "capture", Action: "record", Provenance: "external", SourcePlanSHA256: tool.DigestBytes(original), PlanSHA256: tool.DigestBytes(plan), ManifestSHA256: manifestHash, RecordingSHA256: tool.DigestBytes(nil), Connections: []string{"main"}}
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "material.json"), material))
	before, err := history.HashTree(source)
	require.NoError(t, err)
	runtimePlan := tool.Object{"provider": "codex"}
	require.ErrorContains(t, PrepareHistorySeed(source, target, runtimePlan), "qualified complete package")
	// Materialization of native bytes is independent of official business eligibility.
	_, err = history.MaterializeSeed(source, target, runtimePlan)
	require.NoError(t, err)
	after, err := history.HashTree(source)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for name, data := range nativeFiles {
		actual, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(name)))
		require.NoError(t, err)
		require.True(t, bytes.Equal(data, actual))
	}
	require.NoFileExists(t, filepath.Join(target, "history-seed", "project", "fixture.txt"))
	require.NoDirExists(t, filepath.Join(target, "history-seed", "source"))
	require.NoDirExists(t, filepath.Join(target, "history-seed", "config"))
}
