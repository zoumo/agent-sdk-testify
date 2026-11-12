package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

// These synthetic peers use the real record, verify, qualification and adoption
// writers. Their output proves offline contract wiring, never live SDK success.
func TestFinalContractsQualifiedChildHistorySeed(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) { checkQualifiedChildHistorySeed(t, provider) })
	}
}

func checkQualifiedChildHistorySeed(t *testing.T, provider string) {
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	_, drivers, env := isolatedCommands(t, t.TempDir(), provider)
	agent := filepath.Join(t.TempDir(), "synthetic-history-agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!"+python+"\n"+`
import json, os, pathlib, sys, re
if sys.argv[1:] == ['--version']:
    print('synthetic-history-only')
    sys.exit(0)
config = pathlib.Path(os.environ.get('CODEX_HOME', os.environ.get('CLAUDE_CONFIG_DIR')))
if 'CODEX_HOME' in os.environ:
    for identity in ['parent', 'child']:
        p = config / 'sessions' / (identity + '.jsonl')
        p.parent.mkdir(parents=True, exist_ok=True)
        payload = {'id': identity, 'cwd': os.getcwd()}
        if identity == 'child': payload['parentThreadId'] = 'parent'
        p.write_text(json.dumps({'type':'session_meta', 'payload':payload})+'\n')
else:
    key = re.sub(r'[^a-zA-Z0-9]', '-', os.getcwd())
    for name, data in [('parent.jsonl', {'sessionId':'parent', 'cwd':os.getcwd()}), ('parent/subagents/agent-child.jsonl', {'sessionId':'parent', 'agentId':'child', 'cwd':os.getcwd()})]:
        p = config / 'projects' / key / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(json.dumps(data)+'\n')
for line in sys.stdin:
    request = json.loads(line)
    print(json.dumps({'id':request['id'], 'result':{'synthetic':True}}), flush=True)
`), 0o700))
	driver := filepath.Join(drivers, "agent-sdk-testify-"+provider)
	require.NoError(t, os.WriteFile(driver, []byte("#!"+python+"\n"+`
import json, pathlib, subprocess, sys
request = json.loads(pathlib.Path(sys.argv[2]).read_text())
runtime = json.loads(pathlib.Path(request['runtime']).read_text())
plan = json.loads((pathlib.Path(request['materials']) / 'case.json').read_text())
if plan['caseId'] == 'SDK-CASE-035':
    from record_user_story.runtime_config import apply_runtime
    from record_user_story.existing_children import verify_seed
    apply_runtime(plan, request)
    seed = verify_seed(plan)
    import copy
    for key, value in [('sessionId','wrong-parent'), ('provider','wrong-provider')]:
        broken = copy.deepcopy(plan)
        broken['historySeed'][key] = value
        try: verify_seed(broken)
        except ValueError: pass
        else: raise AssertionError('tampered seed identity accepted')
    broken = copy.deepcopy(plan)
    broken['historySeed']['sourcePlan']['sha256'] = '0'*64
    try: verify_seed(broken)
    except ValueError: pass
    else: raise AssertionError('tampered seed hash accepted')
    broken = copy.deepcopy(plan)
    broken['historyBindings'][0]['sha256'] = '0'*64
    try: verify_seed(broken)
    except ValueError: pass
    else: raise AssertionError('tampered history binding accepted')


    assert seed['sessionId'] == 'parent' and seed['agentIds'] == ['child']
    assert len(plan['historyBindings']) >= 2
    if plan['provider'] == 'claude':
        assert plan['args'][plan['args'].index('--resume')+1] == 'parent'
    # These checks run before the child record process exists.
    (pathlib.Path(runtime['output'])/'parent-seed-validated.json').write_text(json.dumps({'provider':plan['provider'],'seed':seed,'bindings':plan['historyBindings']}))
peer = runtime['peer']
payload = {'id':1, 'method':'conformance', 'params':{'cwd':plan['project']}}
child = subprocess.run([peer['command']]+peer.get('args',[]), input=json.dumps(payload)+'\n', text=True, capture_output=True, timeout=10)
sys.stderr.write(child.stderr)
if child.returncode: sys.exit(child.returncode)
assert json.loads(child.stdout)['result']['synthetic'] is True
report = {'provider':plan['provider'], 'caseId':plan['caseId'], 'storyId':plan['storyId'],
          'status':'passed', 'branchStatus':'passed', 'captureStatus':'complete', 'gaps':[],
          'officialSDK':{'package':{'codex':'openai-codex','claude':'claude-agent-sdk'}[plan['provider']], 'version':'synthetic-contract-only'},
          'receipts':[{'history-query':{'method':'thread/list','params':{'parentThreadId':'parent'},
                      'response':{'data':[{'id':'child','parentThreadId':'parent'}]}}}]}
if plan['provider']=='claude':
    report['receipts']=[{'list_subagents':{'sessionId':'parent','agentIds':['child']}}]
out = pathlib.Path(runtime['output'])
(out / ('replay-report.json' if runtime.get('replay') else 'report.json')).write_text(json.dumps(report))
(out / 'result.json').write_text(json.dumps({'status':'passed'}))
`), 0o700))
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	contents, err := os.ReadFile(driver)
	require.NoError(t, err)
	contents = []byte(strings.Replace(string(contents), "import json, pathlib, subprocess, sys", fmt.Sprintf("import json, pathlib, subprocess, sys\nsys.dont_write_bytecode = True\nsys.path.insert(0, %q)", filepath.Join(repository, "drivers", "common")), 1))
	require.NoError(t, os.WriteFile(driver, contents, 0o700))
	nativeArgs := []string{"app-server", "--listen", "stdio://"}
	if provider == "claude" {
		nativeArgs = []string{"--input-format", "stream-json", "--output-format", "stream-json", "--permission-prompt-tool", "stdio"}
	}
	producer := prepareFixture(t, env, "--agent", provider, "--case", "SDK-CASE-034", "--model", "synthetic-contract-only")
	mustRun(t, env, t.TempDir(), "", append([]string{"official", "record", "--fixture-dir", producer, "--cmd", agent, "--"}, nativeArgs...)...)
	material := latestCapture(t, producer)
	packageRoot := filepath.Join(producer, "packages", str(material["id"]))
	core := filepath.Join(packageRoot, "material")
	require.Equal(t, "captured", readJSON(t, filepath.Join(core, "report.json"))["status"])
	before := officialInventory(t, core)
	plan := readJSON(t, filepath.Join(core, "plan.json"))
	payload, err := json.Marshal(object{"id": 1, "method": "conformance", "params": object{"cwd": plan["project"]}})
	require.NoError(t, err)
	ordinary, notice := runAgentResult(t, env, producer, string(payload)+"\n", "mock", append([]string{"--"}, nativeArgs...)...)
	require.Zero(t, ordinary.code, ordinary.stderr)
	ordinaryID := str(notice["executionId"])
	ordinaryRoot := filepath.Join(packageRoot, "executions", ordinaryID)
	ordinaryAggregate := readJSON(t, filepath.Join(ordinaryRoot, "result.json"))
	for _, name := range []string{"isolation.json", "prepared-plan.json"} {
		require.Contains(t, obj(ordinaryAggregate["files"]), "executions/"+ordinaryID+"/"+name)
	}
	require.NoDirExists(t, filepath.Join(packageRoot, "verify"))
	assertFileHashes(t, packageRoot, obj(ordinaryAggregate["files"]))
	corpus := newSyntheticCorpus(t)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", producer, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	verifyRoot, verification := latestOfficialVerification(t, producer)
	require.NoFileExists(t, filepath.Join(verifyRoot, "mock-isolation.json"))
	mockID := str(obj(array(verification["executions"])[0])["executionId"])
	require.NotEqual(t, ordinaryID, mockID, "verification must bind only its own actual execution")
	mockRoot := filepath.Join(packageRoot, "executions", mockID)
	aggregate := readJSON(t, filepath.Join(mockRoot, "result.json"))
	for _, name := range []string{"isolation.json", "prepared-plan.json"} {
		require.Contains(t, obj(aggregate["files"]), "executions/"+mockID+"/"+name)
		// The same allocation writer creates both shapes; their identities and
		// observed overlay values are specific to each execution.
		require.Len(t, readJSON(t, filepath.Join(mockRoot, name)), len(readJSON(t, filepath.Join(ordinaryRoot, name))))
		original := readFile(t, filepath.Join(mockRoot, name))
		require.NoError(t, os.WriteFile(filepath.Join(mockRoot, name), []byte("{}\n"), 0o600))
		rejected := runTool(t, env, t.TempDir(), "", "prepare", "--recording", packageRoot, "--fixture-dir", filepath.Join(t.TempDir(), "tampered-mock"))
		require.Equal(t, 1, rejected.code)
		require.NoError(t, os.WriteFile(filepath.Join(mockRoot, name), original, 0o600))
	}
	assertFileHashes(t, packageRoot, obj(aggregate["files"]))
	require.Equal(t, before, officialInventory(t, core))
	var attempt string
	for _, value := range array(readJSON(t, corpus.index)["cases"]) {
		entry := obj(value)
		if entry["provider"] == provider && entry["caseId"] == "SDK-CASE-034" {
			require.Equal(t, "admitted", entry["adoption"])
			attempt = filepath.Join(filepath.Dir(corpus.index), str(entry["materials"]))
		}
	}
	require.NotEmpty(t, attempt)
	seedBefore := officialInventory(t, attempt)
	seedFixture := prepareFixture(t, env, "--agent", provider, "--case", "SDK-CASE-035", "--history-seed", attempt)
	sourceBytes := readFile(t, filepath.Join(seedFixture, "source", "plan.json"))
	require.NotContains(t, string(sourceBytes), "historySeed")
	require.Equal(t, seedBefore, officialInventory(t, attempt))
	seedPlan := readJSON(t, filepath.Join(attempt, "material", "plan.json"))
	for _, value := range array(seedPlan["historyBindings"]) {
		binding := obj(value)
		if provider == "codex" {
			for _, location := range []string{"baseline", filepath.Join("templates", "record")} {
				require.Equal(t, readFile(t, filepath.Join(attempt, "material", str(binding["source"]))), readFile(t, filepath.Join(seedFixture, "runtime", location, str(binding["nativeTarget"]))))
			}
		}
	}

	require.Equal(t, "captured", readJSON(t, filepath.Join(attempt, "material", "report.json"))["status"])
	// Standalone exported core has no selected business facts, even though its
	// native history, source plan and capture inventory are complete.
	rejected := runTool(t, env, t.TempDir(), "", "prepare", "--agent", provider, "--case", "SDK-CASE-035", "--history-seed", core, "--fixture-dir", filepath.Join(t.TempDir(), "rejected"))
	require.Equal(t, 1, rejected.code)
	require.Contains(t, rejected.stderr, "qualified complete package")
	reviewPath := filepath.Join(attempt, "review.json")
	oldReview := readFile(t, reviewPath)
	review := readJSON(t, reviewPath)
	obj(review["record"])["path"] = "record/unrelated/result.json"
	writeJSON(t, reviewPath, review)
	rejected = runTool(t, env, t.TempDir(), "", "prepare", "--agent", provider, "--case", "SDK-CASE-035", "--history-seed", attempt, "--fixture-dir", filepath.Join(t.TempDir(), "wrong-selection"))
	require.Equal(t, 1, rejected.code)
	require.NoError(t, os.WriteFile(reviewPath, oldReview, 0o600))
	// Delete every external source. Both source closure and route history remain
	// available from the new fixture; a new record uses only the frozen seed.
	require.NoError(t, os.RemoveAll(producer))
	require.NoError(t, os.RemoveAll(attempt))
	mustRun(t, env, t.TempDir(), "", append([]string{"official", "record", "--fixture-dir", seedFixture, "--cmd", agent, "--"}, nativeArgs...)...)
	require.Equal(t, sourceBytes, readFile(t, filepath.Join(seedFixture, "source", "plan.json")))
	recordRoots, err := filepath.Glob(filepath.Join(seedFixture, "packages", "*", "record", "*", "parent-seed-validated.json"))
	require.NoError(t, err)
	require.Len(t, recordRoots, 1)
	parent := readJSON(t, recordRoots[0])
	require.Equal(t, provider, parent["provider"])
	for _, binding := range array(parent["bindings"]) {
		require.True(t, strings.HasPrefix(str(obj(binding)["source"]), "history/seed/"))
	}
	seededCore := fixtureMaterialRoot(t, seedFixture, latestCapture(t, seedFixture))
	seededPlan := readJSON(t, filepath.Join(seededCore, "plan.json"))
	require.Equal(t, "parent", obj(seededPlan["historySeed"])["sessionId"])
	require.Equal(t, []any{"child"}, obj(seededPlan["historySeed"])["agentIds"])
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", seedFixture)

	if target := os.Getenv("TESTIFY_FINAL_CONTRACT_INVENTORY"); target != "" {
		target += "-" + provider
		require.NoError(t, writeJSONFile(target, object{"seedPackage": seedBefore, "ordinaryMockExecution": ordinaryAggregate, "officialMockExecution": aggregate, "seededCore": officialInventory(t, seededCore)}))
	}
}
