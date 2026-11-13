package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Explicit synthetic legacy inputs preserve rollout names and Claude's sidecar
// coverage independently of the current recording corpus.
func TestFinalContractsLegacyChildHistorySeed(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) { checkLegacyChildHistorySeed(t, provider) })
	}
}

func checkLegacyChildHistorySeed(t *testing.T, provider string) {
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	original := syntheticLegacyChildHistory(t, provider)
	require.NotContains(t, readJSON(t, filepath.Join(original, "plan.json")), "historyBindings")
	inventory := officialInventory(t, original)
	external := filepath.Join(t.TempDir(), "legacy-source")
	require.NoError(t, tool.CopyTree(original, external))
	require.Equal(t, inventory, officialInventory(t, external))
	priorHash := digestFile(t, filepath.Join(external, "plan.json"))
	agent, drivers, env := isolatedCommands(t, t.TempDir(), provider)
	require.NoError(t, os.WriteFile(agent, []byte("#!"+python+"\n"+`
import json, os, pathlib, re, sys
if sys.argv[1:] == ['--version']:
    print('synthetic-legacy-contract-only')
    sys.exit(0)
for line in sys.stdin:
    request = json.loads(line)
    if 'CLAUDE_CONFIG_DIR' in os.environ:
        parent, children = request['params']['parent'], request['params']['children']
        assert sys.argv[sys.argv.index('--resume')+1] == parent
        project = pathlib.Path(os.environ['CLAUDE_CONFIG_DIR']) / 'projects' / re.sub(r'[^a-zA-Z0-9]', '-', os.getcwd())
        names = [parent + '.jsonl'] + [parent + '/subagents/agent-' + child + '.jsonl' for child in children]
        for name in names:
            rows = [json.loads(row) for row in (project / name).read_text().splitlines() if row.strip()]
            assert any(row.get('cwd') == os.getcwd() for row in rows)
            assert all(row.get('sessionId', parent) == parent for row in rows)
        for child in children:
            assert (project / parent / 'subagents' / ('agent-' + child + '.meta.json')).is_file()
    print(json.dumps({'id':request['id'], 'result':{'synthetic':True}}), flush=True)
`), 0o700))
	driverCode := fmt.Sprintf(`
import json, pathlib, subprocess, sys
sys.dont_write_bytecode = True
sys.path.insert(0, %q)
from record_user_story.runtime_config import apply_runtime
from record_user_story.existing_children import verify_seed
request = json.loads(pathlib.Path(sys.argv[2]).read_text())
plan = json.loads((pathlib.Path(request['materials']) / 'case.json').read_text())
runtime = apply_runtime(plan, request)
seed = verify_seed(plan)
assert len(seed['files']) == 2
if runtime['replay']:
    assert 'files' not in plan['historySeed']
    assert len(plan['historyBindings']) >= 2
peer = runtime['peer']
payload = {'id':1, 'method':'synthetic-legacy-observation', 'params':{'parent':seed['sessionId'], 'children':seed['agentIds']}}
child = subprocess.run([peer['command']]+peer.get('args',[]), input=json.dumps(payload)+'\n', text=True, capture_output=True, timeout=15)
sys.stderr.write(child.stderr)
if child.returncode: sys.exit(child.returncode)
assert json.loads(child.stdout)['result']['synthetic'] is True
verify_seed(plan)
report = {'provider':plan['provider'], 'caseId':plan['caseId'], 'storyId':plan['storyId'],
          'status':'passed', 'branchStatus':'passed', 'captureStatus':'complete', 'gaps':[],
          'officialSDK':{'package':{'codex':'openai-codex','claude':'claude-agent-sdk'}[plan['provider']], 'version':'synthetic-legacy-contract-only'}}
out = pathlib.Path(runtime['output'])
(out / 'validated-seed.json').write_text(json.dumps(seed))
(out / ('replay-report.json' if runtime['replay'] else 'report.json')).write_text(json.dumps(report))
(out / 'result.json').write_text(json.dumps({'status':'passed'}))
`, filepath.Join(repository, "drivers", "common"))
	require.NoError(t, os.WriteFile(filepath.Join(drivers, "agent-sdk-testify-"+provider), []byte("#!"+python+"\n"+driverCode), 0o700))
	fixture := prepareFixture(t, env, "--agent", provider, "--case", "SDK-CASE-035", "--history-seed", external)
	require.Equal(t, inventory, officialInventory(t, filepath.Join(fixture, "source", "history-seed", "source")))
	require.NoError(t, os.RemoveAll(external))
	nativeArgs := []string{"app-server", "--listen", "stdio://"}
	if provider == "claude" {
		nativeArgs = []string{"--input-format", "stream-json", "--output-format", "stream-json", "--permission-prompt-tool", "stdio"}
	}
	mustRun(t, env, t.TempDir(), "", append([]string{"official", "record", "--fixture-dir", fixture, "--cmd", agent, "--"}, nativeArgs...)...)
	core := fixtureMaterialRoot(t, fixture, latestCapture(t, fixture))
	plan := readJSON(t, filepath.Join(core, "plan.json"))
	seed := obj(plan["historySeed"])
	for _, key := range []string{"files", "sourceFiles", "projectFiles", "sourceRoot"} {
		require.NotContains(t, seed, key)
	}
	require.Equal(t, 3, int(readJSON(t, filepath.Join(core, "manifest.json"))["materialFormatVersion"].(float64)))
	require.Equal(t, "history/seed/source/plan.json", obj(seed["materialPlan"])["path"])
	require.Equal(t, priorHash, obj(seed["materialPlan"])["sha256"])
	require.Equal(t, digestFile(t, filepath.Join(fixture, "source", "plan.json")), plan["sourcePlanSHA256"])
	require.NotEqual(t, priorHash, plan["sourcePlanSHA256"])
	require.Equal(t, inventory, officialInventory(t, filepath.Join(core, "history", "seed", "source")))
	saved := obj(readJSON(t, filepath.Join(original, "manifest.json"))["savedFiles"])
	require.Len(t, array(plan["historyBindings"]), len(saved))
	for _, value := range array(plan["historyBindings"]) {
		binding := obj(value)
		target := str(binding["nativeTarget"])
		require.Equal(t, "seed", binding["role"])
		require.Equal(t, "history/seed/source/"+target, binding["source"])
		require.Equal(t, saved[target], binding["sha256"])
	}
	before := officialInventory(t, core)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixture)
	require.Equal(t, before, officialInventory(t, core))
	require.Equal(t, inventory, officialInventory(t, original))
	if target := os.Getenv("TESTIFY_LEGACY_SEED_INVENTORY"); target != "" {
		require.NoError(t, writeJSONFile(target+"-"+provider+".json", object{"source": inventory, "core": before, "plan": plan}))
	}
}

func syntheticLegacyChildHistory(t *testing.T, provider string) string {
	t.Helper()
	root := t.TempDir()
	const project = "/synthetic/legacy/project"
	const parent, child = "legacy-parent", "legacy-child"
	plan := object{
		"provider": provider, "driverProfile": "history", "project": project,
		"model": "synthetic-model", "recordingIsolation": "personal-skills-v1",
		"scenario": object{
			"parameters": object{"historyTarget": "child-messages"},
			"files":      object{"fixture.txt": "synthetic legacy seed\n"},
		},
	}
	writeJSON(t, filepath.Join(root, "plan.json"), plan)
	receipts := []object{{"list_subagents": object{"sessionId": parent, "agentIds": []string{child}}}}
	history := map[string]object{
		"config/projects/-synthetic-legacy-project/" + parent + ".jsonl":                                   {"cwd": project, "sessionId": parent},
		"config/projects/-synthetic-legacy-project/" + parent + "/subagents/agent-" + child + ".jsonl":     {"cwd": project, "sessionId": parent, "agentId": child},
		"config/projects/-synthetic-legacy-project/" + parent + "/subagents/agent-" + child + ".meta.json": {"agentType": "fixture_reader"},
	}
	if provider == "codex" {
		receipts = []object{{"history-query": object{
			"method":   "thread/list",
			"params":   object{"parentThreadId": parent},
			"response": object{"data": []object{{"id": child, "parentThreadId": parent}}},
		}}}
		history = map[string]object{
			"config/sessions/2026/11/01/rollout-" + parent + ".jsonl": {"type": "session_meta", "payload": object{"id": parent, "cwd": project}},
			"config/sessions/2026/11/01/rollout-" + child + ".jsonl":  {"type": "session_meta", "payload": object{"id": child, "cwd": project, "parentThreadId": parent}},
		}
	}
	saved := object{}
	for name, row := range history {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		data, err := json.Marshal(row)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
		saved[name] = digestFile(t, path)
	}
	writeJSON(t, filepath.Join(root, "report.json"), object{"status": "passed", "provenance": "synthetic", "receipts": receipts})
	require.NoError(t, os.MkdirAll(filepath.Join(root, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte("{}\n"), 0o600))
	writeJSON(t, filepath.Join(root, "manifest.json"), object{
		"provenance": "capture", "status": "passed", "captureStatus": "complete",
		"planSHA256": digestFile(t, filepath.Join(root, "plan.json")), "savedFiles": saved,
		"files": object{"raw.jsonl": digestFile(t, filepath.Join(root, "raw", "raw.jsonl"))},
	})
	return root
}
