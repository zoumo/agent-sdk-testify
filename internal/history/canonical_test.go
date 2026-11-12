package history

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func writeHistory(t *testing.T, root, name, data string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
	require.NoError(t, os.WriteFile(target, []byte(data), 0o600))
}

func canonicalObjects(t *testing.T, root string, bindings []Binding) (tool.Object, tool.Object) {
	t.Helper()
	files, err := HashTree(root)
	require.NoError(t, err)
	encoded, err := json.Marshal(bindings)
	require.NoError(t, err)
	var decoded []any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	values := tool.Object{}
	for name, hash := range files {
		values[name] = hash
	}
	return tool.Object{"historyBindings": decoded}, tool.Object{"files": values}
}

func TestCanonicalHistoryRetainsClosureAndReusesOnlySameTarget(t *testing.T) {
	seed, overlay, core := t.TempDir(), t.TempDir(), t.TempDir()
	const target = "config/sessions/2026/11/12/rollout-parent.jsonl"
	const original = "{\"type\":\"session_meta\",\"payload\":{\"id\":\"parent\",\"cwd\":\"/original\",\"unknown\":9007199254740993}}\n"
	writeHistory(t, seed, target, original)
	writeHistory(t, seed, "plan.json", "{\"original\":true}\n")
	writeHistory(t, seed, "report.json", "{\"status\":\"passed\"}\n")
	writeHistory(t, seed, "raw/stderr.raw", "")
	writeHistory(t, overlay, target, original)
	bindings, err := CaptureCanonical(seed, overlay, core, tool.Object{"project": "/original"}, nil)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	require.Equal(t, "seed", bindings[0].Role)
	require.Equal(t, "history/seed/"+target, bindings[0].Source)
	require.NoFileExists(t, filepath.Join(core, "history", "runtime", target))
	before, err := HashTree(seed)
	require.NoError(t, err)
	after, err := HashTree(filepath.Join(core, "history", "seed"))
	require.NoError(t, err)
	require.Equal(t, before, after)

	// Same identity with new runtime bytes retains the entire original closure.
	changed := original + "{\"type\":\"event_msg\",\"payload\":{\"text\":\"new\"}}\n"
	writeHistory(t, overlay, target, changed)
	changedCore := t.TempDir()
	bindings, err = CaptureCanonical(seed, overlay, changedCore, tool.Object{"project": "/original"}, nil)
	require.NoError(t, err)
	require.Equal(t, "runtime", bindings[0].Role)
	require.Equal(t, tool.DigestBytes([]byte(changed)), bindings[0].SHA256)
	require.True(t, bytes.Equal([]byte(original), readHistory(t, filepath.Join(changedCore, "history", "seed", target))))
	plan, manifest := canonicalObjects(t, changedCore, bindings)
	_, err = CanonicalBindings(changedCore, plan, manifest)
	require.NoError(t, err)
}

func TestCanonicalHistoryRejectsConflictingBindings(t *testing.T) {
	const target = "config/sessions/rollout-child.jsonl"
	const data = "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\",\"cwd\":\"/original\",\"parentThreadId\":\"parent\"}}\n"
	for _, tc := range []struct {
		name   string
		mutate func(*Binding)
	}{
		{"absolute source", func(b *Binding) { b.Source = "/history/runtime/" + target }},
		{"escape source", func(b *Binding) { b.Source = "history/runtime/../" + target }},
		{"unclean source", func(b *Binding) { b.Source = "history/runtime//" + target }},
		{"backslash source", func(b *Binding) { b.Source = "history\\runtime/" + target }},
		{"wrong destination", func(b *Binding) { b.NativeTarget = "config/sessions/other.jsonl" }},
		{"wrong identity", func(b *Binding) { b.SessionID = "other" }},
		{"wrong parent", func(b *Binding) { b.ParentSessionID = "other" }},
		{"wrong hash", func(b *Binding) { b.SHA256 = tool.DigestBytes(nil) }},
		{"unknown role", func(b *Binding) { b.Role = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			name := "history/runtime/" + target
			writeHistory(t, root, name, data)
			binding := Binding{Role: "runtime", SessionID: "child", ParentSessionID: "parent", ChildSessionID: "child", Project: "/original", NativeTarget: target, Source: name, SHA256: tool.DigestBytes([]byte(data))}
			tc.mutate(&binding)
			plan, manifest := canonicalObjects(t, root, []Binding{binding})
			_, err := CanonicalBindings(root, plan, manifest)
			require.Error(t, err)
		})
	}
	t.Run("duplicate target", func(t *testing.T) {
		root := t.TempDir()
		name := "history/runtime/" + target
		writeHistory(t, root, name, data)
		binding := Binding{Role: "runtime", SessionID: "child", ParentSessionID: "parent", ChildSessionID: "child", Project: "/original", NativeTarget: target, Source: name, SHA256: tool.DigestBytes([]byte(data))}
		plan, manifest := canonicalObjects(t, root, []Binding{binding, binding})
		_, err := CanonicalBindings(root, plan, manifest)
		require.ErrorContains(t, err, "duplicate")
	})
	t.Run("unbound runtime", func(t *testing.T) {
		root := t.TempDir()
		writeHistory(t, root, "history/runtime/"+target, data)
		plan, manifest := canonicalObjects(t, root, []Binding{})
		_, err := CanonicalBindings(root, plan, manifest)
		require.ErrorContains(t, err, "unbound runtime")
	})
}

func TestCanonicalClaudeDerivationSealsOnlyChangedBytes(t *testing.T) {
	core, overlay, execution := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "execution")
	require.NoError(t, os.MkdirAll(execution, 0o700))
	target := "config/projects/-original/session.jsonl"
	source := "history/runtime/" + target
	const data = "{\"sessionId\":\"session\",\"cwd\":\"/original\",\"large\":9007199254740993,\"unknown\":{\"keep\":true}}\n"
	writeHistory(t, core, source, data)
	binding := Binding{Role: "runtime", SessionID: "session", Project: "/original", NativeTarget: target, Source: source, SHA256: tool.DigestBytes([]byte(data))}
	plan, manifest := canonicalObjects(t, core, []Binding{binding})
	plan["provider"] = "claude"
	files, err := materializeCanonical(core, overlay, plan, manifest)
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.NoError(t, SealMaterialization(overlay, execution, "material"))
	var receipt tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(execution, "materialization.json"), &receipt))
	require.EqualValues(t, 2, receipt["version"])
	items := tool.Array(receipt["transformations"])
	require.Len(t, items, 1)
	item := tool.Obj(items[0])
	require.Equal(t, "material/"+source, item["source"])
	require.Equal(t, binding.SHA256, item["sourceSHA256"])
	require.Equal(t, "/original", item["sourceCWD"])
	require.Equal(t, filepath.Join(overlay, "workspace"), item["executionCWD"])
	derived := filepath.Join(filepath.Dir(execution), filepath.FromSlash(tool.Str(item["derived"]))[len("executions/"):])
	require.Equal(t, tool.Str(item["derivedSHA256"]), tool.DigestBytes(readHistory(t, derived)))
	require.Contains(t, string(readHistory(t, derived)), "9007199254740993")
	require.Contains(t, string(readHistory(t, derived)), `"unknown":{"keep":true}`)
	require.Equal(t, byte('\n'), readHistory(t, derived)[len(readHistory(t, derived))-1])
	require.True(t, bytes.Equal([]byte(data), readHistory(t, filepath.Join(core, source))))
	manifest["materialFormatVersion"] = 3
	require.NoError(t, tool.WriteJSON(filepath.Join(core, "plan.json"), plan))
	require.NoError(t, tool.WriteJSON(filepath.Join(core, "manifest.json"), manifest))
	startup := t.TempDir()
	startupFiles, err := MaterializeStartup(core, startup)
	require.NoError(t, err)
	require.Len(t, startupFiles, 2)
	require.NoFileExists(t, filepath.Join(startup, "materialization.json"))
	require.NoDirExists(t, filepath.Join(startup, "history", "derived"))
	sourceInfo, err := os.Stat(filepath.Join(core, source))
	require.NoError(t, err)
	targetInfo, err := os.Lstat(filepath.Join(startup, target))
	require.NoError(t, err)
	require.True(t, targetInfo.Mode().IsRegular())
	require.False(t, os.SameFile(sourceInfo, targetInfo))
	require.NoError(t, Check(overlay, files))
	writeHistory(t, overlay, tool.Str(item["nativeTarget"]), "{}\n")
	require.ErrorContains(t, Check(overlay, files), "hash mismatch")
}

func TestCanonicalClaudeUpdatedSeedUsesRuntimeDerivation(t *testing.T) {
	core, overlay := t.TempDir(), t.TempDir()
	const seed = "{\"sessionId\":\"session\",\"cwd\":\"/seed\",\"large\":9007199254740993}\n"
	const runtime = "{\"sessionId\":\"session\",\"cwd\":\"/recorded\",\"large\":9007199254740993}\n{\"sessionId\":\"session\",\"cwd\":\"/recorded\",\"text\":\"new output\"}\n"
	bindings := []Binding{
		{Role: "seed", SessionID: "session", Project: "/seed", NativeTarget: "config/projects/-seed/session.jsonl", Source: "history/seed/config/projects/-seed/session.jsonl", SHA256: tool.DigestBytes([]byte(seed))},
		{Role: "runtime", SessionID: "session", Project: "/recorded", NativeTarget: "config/projects/-recorded/session.jsonl", Source: "history/runtime/config/projects/-recorded/session.jsonl", SHA256: tool.DigestBytes([]byte(runtime))},
	}
	writeHistory(t, core, bindings[0].Source, seed)
	writeHistory(t, core, bindings[1].Source, runtime)
	plan, manifest := canonicalObjects(t, core, bindings)
	plan["provider"] = "claude"
	files, err := materializeCanonical(core, overlay, plan, manifest)
	require.NoError(t, err)
	require.Len(t, files, 3)
	require.NoError(t, Check(overlay, files))
	require.True(t, bytes.Equal([]byte(seed), readHistory(t, filepath.Join(core, bindings[0].Source))))
	require.True(t, bytes.Equal([]byte(runtime), readHistory(t, filepath.Join(core, bindings[1].Source))))
	var receipt tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(overlay, "materialization.json"), &receipt))
	items := tool.Array(receipt["transformations"])
	require.Len(t, items, 1)
	derived := tool.Obj(items[0])
	require.Equal(t, bindings[1].Source, derived["source"])
	data := string(readHistory(t, filepath.Join(overlay, tool.Str(derived["derived"]))))
	require.Contains(t, data, `"text":"new output"`)
	require.Contains(t, data, "9007199254740993")
	plan, manifest = canonicalObjects(t, core, []Binding{bindings[1], bindings[0]})
	plan["provider"] = "claude"
	reversed := t.TempDir()
	_, err = materializeCanonical(core, reversed, plan, manifest)
	require.NoError(t, err)
	require.NoError(t, tool.ReadJSON(filepath.Join(reversed, "materialization.json"), &receipt))
	require.Equal(t, bindings[1].Source, tool.Obj(tool.Array(receipt["transformations"])[0])["source"])
}

func TestCanonicalClaudeRejectsAmbiguousProjectDerivations(t *testing.T) {
	for _, role := range []string{"seed", "runtime"} {
		t.Run(role, func(t *testing.T) {
			core := t.TempDir()
			var bindings []Binding
			for _, project := range []string{"/first", "/second"} {
				data := "{\"sessionId\":\"session\",\"cwd\":\"" + project + "\"}\n"
				target := "config/projects/-" + filepath.Base(project) + "/session.jsonl"
				binding := Binding{Role: role, SessionID: "session", Project: project, NativeTarget: target, Source: "history/" + role + "/" + target, SHA256: tool.DigestBytes([]byte(data))}
				writeHistory(t, core, binding.Source, data)
				bindings = append(bindings, binding)
			}
			plan, manifest := canonicalObjects(t, core, bindings)
			plan["provider"] = "claude"
			_, err := materializeCanonical(core, t.TempDir(), plan, manifest)
			require.ErrorContains(t, err, "ambiguous Claude history derivation target")
		})
	}
}

func readHistory(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	require.NoError(t, err)
	return data
}

func TestRecordClaudeMaterializationRetainsFailureSourceAndRebindsSuccess(t *testing.T) {
	pkg, overlay := t.TempDir(), t.TempDir()
	execution := filepath.Join(pkg, "executions", "record")
	source := filepath.Join(execution, "capture", "history-source", "source")
	target := "config/projects/-original/session.jsonl"
	const data = "{\"sessionId\":\"session\",\"cwd\":\"/original\",\"large\":9007199254740993}\n"
	writeHistory(t, source, target, data)
	writeHistory(t, source, "manifest.json", "{}")
	writeHistory(t, source, "plan.json", "{\"provider\":\"claude\",\"project\":\"/original\"}")
	files, err := HashTree(source)
	require.NoError(t, err)
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), tool.Object{"savedFiles": tool.Object{target: files[target]}}))
	_, err = Materialize(source, overlay)
	require.NoError(t, err)
	require.NoError(t, SealMaterialization(overlay, execution, "executions/record/capture/history-source/source"))
	require.NoError(t, ValidateMaterialization(pkg, execution))
	// A failed record can be verified after the entire runtime overlay is gone.
	require.NoError(t, os.RemoveAll(overlay))
	require.NoError(t, ValidateMaterialization(pkg, execution))

	// Packaging moves the complete original closure, then publication completes
	// the reference before the execution envelope is sealed.
	seed := filepath.Join(execution, "capture", "history", "seed")
	require.NoError(t, os.MkdirAll(filepath.Dir(seed), 0o700))
	require.NoError(t, os.Rename(filepath.Dir(source), seed))
	require.NoError(t, RebindMaterialization(pkg, execution, "executions/record/capture/history-source", "executions/record/capture/history/seed"))
	require.NoError(t, os.Rename(filepath.Join(execution, "capture"), filepath.Join(pkg, "material")))
	require.NoError(t, RebindMaterialization(pkg, execution, "executions/record/capture/history/seed", "material/history/seed"))
	require.NoError(t, ValidateMaterialization(pkg, execution))
	derived := filepath.Join(execution, "history", "derived")
	hashes, err := HashTree(derived)
	require.NoError(t, err)
	for name := range hashes {
		writeHistory(t, derived, name, "{}\n")
	}
	require.Error(t, ValidateMaterialization(pkg, execution))
}

func TestClaudeMetadataUsesNativeChildIdentity(t *testing.T) {
	b, err := Identify("config/projects/-original/parent/subagents/agent-child.meta.json", []byte("{\"agentType\":\"general-purpose\"}\n"), "/original")
	require.NoError(t, err)
	require.Equal(t, "parent", b.ParentSessionID)
	require.Equal(t, "child", b.ChildSessionID)
	require.Equal(t, "child", b.SessionID)
}

func TestCaptureClaudeMetadataUsesExecutionProject(t *testing.T) {
	overlay, core := t.TempDir(), t.TempDir()
	project := filepath.Join(overlay, "workspace")
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(project, "-")
	target := "config/projects/" + key + "/parent/subagents/agent-child.meta.json"
	const data = "{\"agentType\":\"general-purpose\"}\n"
	writeHistory(t, overlay, target, data)
	bindings, err := CaptureCanonical("", overlay, core, tool.Object{"provider": "claude", "project": "/stable/route/workspace"}, nil)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	require.Equal(t, project, bindings[0].Project)
	require.Equal(t, "parent", bindings[0].ParentSessionID)
	require.Equal(t, "child", bindings[0].ChildSessionID)
	require.Equal(t, target, bindings[0].NativeTarget)
	require.True(t, bytes.Equal([]byte(data), readHistory(t, filepath.Join(core, bindings[0].Source))))
	plan, manifest := canonicalObjects(t, core, bindings)
	_, err = CanonicalBindings(core, plan, manifest)
	require.NoError(t, err)

	seed := t.TempDir()
	original := Binding{Role: "runtime", SessionID: "old-child", ParentSessionID: "old-parent", ChildSessionID: "old-child", Project: "/seed", NativeTarget: "config/projects/-seed/old-parent/subagents/agent-old-child.meta.json", SHA256: tool.DigestBytes([]byte(data))}
	original.Source = "history/runtime/" + original.NativeTarget
	writeHistory(t, seed, original.Source, data)
	seedPlan, seedManifest := canonicalObjects(t, seed, []Binding{original})
	seedManifest["materialFormatVersion"] = 3
	require.NoError(t, tool.WriteJSON(filepath.Join(seed, "plan.json"), seedPlan))
	require.NoError(t, tool.WriteJSON(filepath.Join(seed, "manifest.json"), seedManifest))
	writeHistory(t, overlay, original.NativeTarget, data)
	bindings, err = CaptureCanonical(seed, overlay, t.TempDir(), tool.Object{"provider": "claude", "project": "/stable/route/workspace"}, nil)
	require.NoError(t, err)
	require.Len(t, bindings, 2)
	for _, binding := range bindings {
		if binding.NativeTarget == original.NativeTarget {
			require.Equal(t, "seed", binding.Role)
			require.Equal(t, original.Project, binding.Project)
		} else {
			require.Equal(t, project, binding.Project)
		}
	}
}
