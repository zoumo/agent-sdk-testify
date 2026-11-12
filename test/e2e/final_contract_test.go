package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// These synthetic peers use the real record, verify, qualification and adoption
// writers. Their output proves offline contract wiring, never live SDK success.
func TestFinalContractsQualifiedChildHistorySeed(t *testing.T) {
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	_, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	agent := filepath.Join(t.TempDir(), "synthetic-history-agent")
	require.NoError(t, os.WriteFile(agent, []byte("#!"+python+"\n"+`
import json, os, pathlib, sys
if sys.argv[1:] == ['--version']:
    print('synthetic-history-only')
    sys.exit(0)
config = pathlib.Path(os.environ['CODEX_HOME'])
for identity in ['parent', 'child']:
    p = config / 'sessions' / (identity + '.jsonl')
    p.parent.mkdir(parents=True, exist_ok=True)
    payload = {'id': identity, 'cwd': os.getcwd()}
    if identity == 'child': payload['parentThreadId'] = 'parent'
    p.write_text(json.dumps({'type':'session_meta', 'payload':payload})+'\n')
for line in sys.stdin:
    request = json.loads(line)
    print(json.dumps({'id':request['id'], 'result':{'synthetic':True}}), flush=True)
`), 0o700))
	driver := filepath.Join(drivers, "agent-sdk-testify-codex")
	require.NoError(t, os.WriteFile(driver, []byte("#!"+python+"\n"+`
import json, pathlib, subprocess, sys
request = json.loads(pathlib.Path(sys.argv[2]).read_text())
runtime = json.loads(pathlib.Path(request['runtime']).read_text())
plan = json.loads((pathlib.Path(request['materials']) / 'case.json').read_text())
peer = runtime['peer']
payload = {'id':1, 'method':'conformance', 'params':{'cwd':plan['project']}}
child = subprocess.run([peer['command']]+peer.get('args',[]), input=json.dumps(payload)+'\n', text=True, capture_output=True, timeout=10)
sys.stderr.write(child.stderr)
if child.returncode: sys.exit(child.returncode)
assert json.loads(child.stdout)['result']['synthetic'] is True
report = {'provider':plan['provider'], 'caseId':plan['caseId'], 'storyId':plan['storyId'],
          'status':'passed', 'branchStatus':'passed', 'captureStatus':'complete', 'gaps':[],
          'officialSDK':{'package':'openai-codex', 'version':'synthetic-contract-only'},
          'receipts':[{'history-query':{'method':'thread/list','params':{'parentThreadId':'parent'},
                      'response':{'data':[{'id':'child','parentThreadId':'parent'}]}}}]}
out = pathlib.Path(runtime['output'])
(out / ('replay-report.json' if runtime.get('replay') else 'report.json')).write_text(json.dumps(report))
(out / 'result.json').write_text(json.dumps({'status':'passed'}))
`), 0o700))
	producer := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-034", "--model", "synthetic-contract-only")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", producer, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	material := latestCapture(t, producer)
	packageRoot := filepath.Join(producer, "packages", str(material["id"]))
	core := filepath.Join(packageRoot, "material")
	require.Equal(t, "captured", readJSON(t, filepath.Join(core, "report.json"))["status"])
	before := officialInventory(t, core)
	corpus := newSyntheticCorpus(t)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", producer, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	verifyRoot, verification := latestOfficialVerification(t, producer)
	require.NoFileExists(t, filepath.Join(verifyRoot, "mock-isolation.json"))
	mockID := str(obj(array(verification["executions"])[0])["executionId"])
	mockRoot := filepath.Join(packageRoot, "executions", mockID)
	aggregate := readJSON(t, filepath.Join(mockRoot, "result.json"))
	for _, name := range []string{"isolation.json", "prepared-plan.json"} {
		require.Contains(t, obj(aggregate["files"]), "executions/"+mockID+"/"+name)
	}
	assertFileHashes(t, packageRoot, obj(aggregate["files"]))
	require.Equal(t, before, officialInventory(t, core))
	var attempt string
	for _, value := range array(readJSON(t, corpus.index)["cases"]) {
		entry := obj(value)
		if entry["provider"] == "codex" && entry["caseId"] == "SDK-CASE-034" {
			require.Equal(t, "admitted", entry["adoption"])
			attempt = filepath.Join(filepath.Dir(corpus.index), str(entry["materials"]))
		}
	}
	require.NotEmpty(t, attempt)
	seedBefore := officialInventory(t, attempt)
	seedFixture := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-035", "--history-seed", attempt)
	require.Equal(t, seedBefore, officialInventory(t, attempt))
	require.Equal(t, "captured", readJSON(t, filepath.Join(attempt, "material", "report.json"))["status"])
	// Standalone exported core has no selected business facts, even though its
	// native history, source plan and capture inventory are complete.
	rejected := runTool(t, env, t.TempDir(), "", "prepare", "--agent", "codex", "--case", "SDK-CASE-035", "--history-seed", core, "--fixture-dir", filepath.Join(t.TempDir(), "rejected"))
	require.Equal(t, 1, rejected.code)
	require.Contains(t, rejected.stderr, "qualified complete package")
	reviewPath := filepath.Join(attempt, "review.json")
	oldReview := readFile(t, reviewPath)
	review := readJSON(t, reviewPath)
	obj(review["record"])["path"] = "record/unrelated/result.json"
	writeJSON(t, reviewPath, review)
	rejected = runTool(t, env, t.TempDir(), "", "prepare", "--agent", "codex", "--case", "SDK-CASE-035", "--history-seed", attempt, "--fixture-dir", filepath.Join(t.TempDir(), "wrong-selection"))
	require.Equal(t, 1, rejected.code)
	require.NoError(t, os.WriteFile(reviewPath, oldReview, 0o600))
	// Delete every external source. Both source closure and route history remain
	// available from the new fixture; a new record uses only the frozen seed.
	require.NoError(t, os.RemoveAll(producer))
	require.NoError(t, os.RemoveAll(attempt))
	mustRun(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", seedFixture, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	seededCore := fixtureMaterialRoot(t, seedFixture, latestCapture(t, seedFixture))
	seededPlan := readJSON(t, filepath.Join(seededCore, "plan.json"))
	require.Equal(t, "parent", obj(seededPlan["historySeed"])["sessionId"])
	require.Equal(t, []any{"child"}, obj(seededPlan["historySeed"])["agentIds"])
	if target := os.Getenv("TESTIFY_FINAL_CONTRACT_INVENTORY"); target != "" {
		require.NoError(t, writeJSONFile(target, object{"seedPackage": seedBefore, "mockExecution": aggregate, "seededCore": officialInventory(t, seededCore)}))
	}
}
