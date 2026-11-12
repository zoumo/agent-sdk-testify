package native_test

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	nativeprep "github.com/zoumo/agent-sdk-testify/internal/native"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Controlled native fixtures exercise startup and saved bytes, not skill loading.
func historyRunner(t *testing.T, provider string) (string, string, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	bin := t.TempDir()
	native, driver := filepath.Join(bin, "native"), filepath.Join(bin, "driver")
	require.NoError(t, os.WriteFile(native, []byte("#!"+python+"\n"+`import json, os, pathlib, sys
if sys.argv[1:] == ['--version']:
    print('controlled-history 1')
    sys.exit(0)
config = pathlib.Path(os.environ.get('CODEX_HOME', os.environ.get('CLAUDE_CONFIG_DIR')))
root = config.parent
plan = json.loads((root / 'plan.json').read_text())
saved = {str(p.relative_to(root)): p.read_text() for p in config.rglob('*.jsonl')}
(root / 'history-observation.json').write_text(json.dumps({'saved': saved, 'cwd': os.getcwd(), 'args': sys.argv[1:]}))
if plan['driverProfile'] == 'history':
    records = {'config/sessions/parent.jsonl': '{"type":"session_meta","payload":{"id":"parent"}}\n',
               'config/sessions/child.jsonl': '{"type":"session_meta","payload":{"id":"child"}}\n'} if plan['provider'] == 'codex' else {
               'config/projects/key/parent.jsonl': '{}\n', 'config/projects/key/parent/subagents/agent-child.jsonl': '{}\n'}
    for name, data in records.items():
        p = root / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(data)
sys.stdin.readline()
print('{"id":1,"result":{}}', flush=True)
sys.stdin.read()
`), 0o700))
	require.NoError(t, os.WriteFile(driver, []byte("#!"+python+"\n"+`import json, pathlib, subprocess, sys
r = json.loads(pathlib.Path(sys.argv[2]).read_text())
runtime = json.loads(pathlib.Path(r['runtime']).read_text())
root = pathlib.Path(runtime['output'])
plan = json.loads((pathlib.Path(r['materials']) / 'case.json').read_text())
p = runtime['peer']
child = subprocess.run([p['command']] + p.get('args', []), input='{"id":1,"method":"ping"}\n', text=True, capture_output=True, timeout=10)
sys.stderr.write(child.stderr)
receipts = [{'history-query': {'method': 'thread/list', 'params': {'parentThreadId': 'parent'}, 'response': {'data': [{'id': 'child', 'parentThreadId': 'parent'}]}}}] if plan['provider'] == 'codex' else [{'list_subagents': {'sessionId': 'parent', 'agentIds': ['child']}}]
(root / 'report.json').write_text(json.dumps({'status': 'passed', 'receipts': receipts}))
(root / 'result.json').write_text(json.dumps({'status': 'passed' if child.returncode == 0 else 'failed'}))
sys.exit(child.returncode)
`), 0o700))
	materials := t.TempDir()
	require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), tool.Object{"provider": provider, "caseId": "controlled-history", "driverProfile": "history", "scenario": tool.Object{"parameters": tool.Object{"historyTarget": "child-messages"}, "files": tool.Object{"fixture.txt": "synthetic source\n"}}}))
	return materials, native, driver
}

func runHistoryCommand(t *testing.T, mode, materials, native, driver string, extra ...string) (string, string, error) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "attempt")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	self, e := os.Executable()
	require.NoError(t, e)
	o := preparationOptions{Options: nativeprep.Options{CLI: native}, Mode: mode, Materials: materials, Driver: driver, Output: output}
	for i := 0; i < len(extra); i += 2 {
		require.Equal(t, "--history-seed", extra[i])
		o.HistorySeed = extra[i+1]
	}
	optionsPath := filepath.Join(t.TempDir(), "options.json")
	require.NoError(t, tool.WriteJSON(optionsPath, o))
	cmd := exec.CommandContext(ctx, self, "native-preparation-fixture", optionsPath)

	data, err := cmd.CombinedOutput()
	return output, string(data), err
}

func TestRecordingDoesNotImportSavedMaterials(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			materials, native, driver := historyRunner(t, provider)
			require.NoError(t, tool.WriteFiles(materials, tool.Object{"config/sessions/legacy.jsonl": "PERSONAL\n", "config/projects/key/legacy.jsonl": "PERSONAL\n", "history-seed/legacy.jsonl": "PERSONAL\n"}))
			var input tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(materials, "plan.json"), &input))
			input["recordingIsolation"] = "fabricated-policy"
			input["historySeed"] = tool.Object{"sessionId": "personal-parent"}
			input["historyProject"] = materials
			require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), input))
			out, log, err := runHistoryCommand(t, "record", materials, native, driver)
			require.NoError(t, err, log)
			var observed tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(out, "history-observation.json"), &observed))
			assert.Empty(t, tool.Obj(observed["saved"]))
			assert.NoFileExists(t, filepath.Join(out, "history-seed", "legacy.jsonl"))
			var recorded tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(out, "plan.json"), &recorded))
			assert.NotContains(t, recorded, "historySeed")
			assert.NotContains(t, recorded, "historyProject")
			assert.Equal(t, "personal-skills-v1", recorded["recordingIsolation"])
			data, err := os.ReadFile(filepath.Join(materials, "config", "sessions", "legacy.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, "PERSONAL\n", string(data))
		})
	}
}

func TestRecordingHistorySeedRejectsUnboundIsolationClaim(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			materials, native, driver := historyRunner(t, provider)
			source, log, err := runHistoryCommand(t, "record", materials, native, driver)
			require.NoError(t, err, log)
			var target, manifest tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(materials, "plan.json"), &target))
			target["driverProfile"] = "existing-children"
			require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), target))
			require.NoError(t, tool.ReadJSON(filepath.Join(source, "manifest.json"), &manifest))
			for _, field := range []string{"planSHA256", "provenance", "captureStatus", "status"} {
				t.Run(field, func(t *testing.T) {
					modified := tool.Object{}
					maps.Copy(modified, manifest)
					delete(modified, field)
					require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), modified))
					out, log, err := runHistoryCommand(t, "record", materials, native, driver, "--history-seed", source)
					require.Error(t, err)
					assert.Contains(t, log, "seed must come from an isolated recording")
					assert.NoFileExists(t, filepath.Join(out, "history-observation.json"))
				})
			}
		})
	}
}

func TestRecordingRejectsEmbeddedHistorySeed(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			materials, native, driver := historyRunner(t, provider)
			var plan tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(materials, "plan.json"), &plan))
			plan["driverProfile"] = "existing-children"
			plan["historySeed"] = tool.Object{"sessionId": "personal-parent", "sourceRoot": materials}
			plan["historyProject"] = materials
			plan["recordingIsolation"] = "personal-skills-v1"
			require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), plan))
			out, log, err := runHistoryCommand(t, "record", materials, native, driver)
			require.Error(t, err)
			assert.Contains(t, log, "existing-child recording needs supplied saved-history materials")
			assert.NoFileExists(t, filepath.Join(out, "history-observation.json"))
		})
	}
}

func TestRecordingHistorySeedRequiresIsolatedCapture(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			materials, native, driver := historyRunner(t, provider)
			source, log, err := runHistoryCommand(t, "record", materials, native, driver)
			require.NoError(t, err, log)
			var previous tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(source, "plan.json"), &previous))
			delete(previous, "recordingIsolation")
			require.NoError(t, tool.WriteJSON(filepath.Join(source, "plan.json"), previous))
			var manifest tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(source, "manifest.json"), &manifest))
			digest, err := tool.FileDigest(filepath.Join(source, "plan.json"))
			require.NoError(t, err)
			manifest["planSHA256"] = digest
			require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), manifest))
			var target tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(materials, "plan.json"), &target))
			target["driverProfile"] = "existing-children"
			require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), target))
			_, log, err = runHistoryCommand(t, "record", materials, native, driver, "--history-seed", source)
			require.Error(t, err)
			assert.Contains(t, log, "seed must come from an isolated recording")
			// Legacy evidence remains valid offline, without certification.
			for _, mode := range []string{"verify"} {
				out, log, err := runHistoryCommand(t, mode, source, native, driver)
				require.NoError(t, err, log)
				var offlinePlan tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(out, "plan.json"), &offlinePlan))
				assert.NotContains(t, offlinePlan, "recordingIsolation")
				for name, digest := range tool.Obj(manifest["savedFiles"]) {
					require.NoError(t, corpus.VerifyFile(source, name, tool.Str(digest)))
					require.NoError(t, corpus.VerifyFile(out, name, tool.Str(digest)))
				}
			}
		})
	}
}

func TestRecordingHistorySeedPreservesEvidenceAndSourceProject(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			materials, native, driver := historyRunner(t, provider)
			source, log, err := runHistoryCommand(t, "record", materials, native, driver)
			require.NoError(t, err, log)
			var sourcePlan, manifest tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(source, "plan.json"), &sourcePlan))
			require.NoError(t, tool.ReadJSON(filepath.Join(source, "manifest.json"), &manifest))
			assert.Equal(t, "personal-skills-v1", sourcePlan["recordingIsolation"])
			before := map[string]string{}
			for _, name := range []string{"plan.json", "manifest.json", "report.json", "raw/raw.jsonl", "project/fixture.txt"} {
				before[name], err = tool.FileDigest(filepath.Join(source, name))
				require.NoError(t, err)
			}
			for name, digest := range tool.Obj(manifest["savedFiles"]) {
				before[name] = tool.Str(digest)
			}
			var target tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(materials, "plan.json"), &target))
			target["driverProfile"] = "existing-children"
			require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), target))
			out, log, err := runHistoryCommand(t, "record", materials, native, driver, "--history-seed", source)
			require.NoError(t, err, log)
			var plan, observed tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(out, "plan.json"), &plan))
			require.NoError(t, tool.ReadJSON(filepath.Join(out, "history-observation.json"), &observed))
			seed := tool.Obj(plan["historySeed"])
			assert.Equal(t, sourcePlan["project"], plan["historyProject"])
			assert.Equal(t, sourcePlan["project"], seed["sourceProject"])
			assert.Equal(t, sourcePlan["project"], observed["cwd"])
			assert.Equal(t, "parent", seed["sessionId"])
			assert.Equal(t, []string{"child"}, tool.Strings(seed["agentIds"]))
			assert.Len(t, tool.Obj(observed["saved"]), 2)
			if provider == "codex" {
				assert.Contains(t, tool.Strings(observed["args"]), "project_root_markers=[]")
				assert.Contains(t, tool.Strings(observed["args"]), "project_doc_max_bytes=0")
			} else {
				assert.Contains(t, tool.Strings(observed["args"]), "--disable-slash-commands")
				assert.Contains(t, tool.Strings(observed["args"]), "--resume")
				assert.Contains(t, tool.Strings(observed["args"]), "parent")
			}
			for name, digest := range before {
				require.NoError(t, corpus.VerifyFile(source, name, digest))
			}
			for name, value := range tool.Obj(seed["files"]) {
				sourceName := strings.TrimPrefix(tool.Str(tool.Obj(value)["source"]), "history/seed/")
				assert.Equal(t, before[sourceName], tool.Obj(value)["sha256"])
				require.NoError(t, corpus.VerifyFile(out, name, tool.Str(tool.Obj(value)["sha256"])))
				require.NoError(t, corpus.VerifyFile(source, sourceName, tool.Str(tool.Obj(value)["sha256"])))
			}
		})
	}
}

func TestRecordingHistorySeedRejectsCodexSourceDiscovery(t *testing.T) {
	materials, native, driver := historyRunner(t, "codex")
	source, log, err := runHistoryCommand(t, "record", materials, native, driver)
	require.NoError(t, err, log)
	var target tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(materials, "plan.json"), &target))
	target["driverProfile"] = "existing-children"
	require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), target))
	for _, name := range []string{".agents", ".codex"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(source, "project", name)
			require.NoError(t, tool.WriteFiles(path, tool.Object{"skills/personal/SKILL.md": "PERSONAL\n"}))
			defer os.RemoveAll(path)
			out, log, err := runHistoryCommand(t, "record", materials, native, driver, "--history-seed", source)
			require.Error(t, err)
			assert.Contains(t, log, "workspace contains "+name)
			assert.NoFileExists(t, filepath.Join(out, "history-observation.json"))
			data, err := os.ReadFile(filepath.Join(path, "skills", "personal", "SKILL.md"))
			require.NoError(t, err)
			assert.Equal(t, "PERSONAL\n", string(data))
		})
	}
}
