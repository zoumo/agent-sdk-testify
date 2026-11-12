package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The legacy capture is synthetic. Its immutable business/capture facts remain
// in the old core while current verification uses the ordinary package owners.
func TestFinalContractsLegacyCaptureAdmission(t *testing.T) {
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	_, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	driver := "#!" + python + "\n" + `
import json, pathlib, subprocess, sys
request = json.loads(pathlib.Path(sys.argv[2]).read_text())
runtime = json.loads(pathlib.Path(request['runtime']).read_text())
plan = json.loads((pathlib.Path(request['materials']) / 'case.json').read_text())
peer = runtime['peer']
child = subprocess.run([peer['command']]+peer.get('args',[]), input='{"id":1,"method":"ping"}\n', text=True, capture_output=True, timeout=10)
sys.stderr.write(child.stderr)
if child.returncode: sys.exit(child.returncode)
assert json.loads(child.stdout) == {'id':1,'result':{}}
report = {'provider':plan['provider'], 'caseId':plan['caseId'], 'storyId':plan['storyId'],
          'status':'passed','branchStatus':'passed','captureStatus':'complete','gaps':[],
          'officialSDK':{'package':'openai-codex','version':'synthetic-legacy-contract'}}
out = pathlib.Path(runtime['output'])
(out / 'replay-report.json').write_text(json.dumps(report))
(out / 'result.json').write_text(json.dumps({'status':'passed'}))
`
	require.NoError(t, os.WriteFile(filepath.Join(drivers, "agent-sdk-testify-codex"), []byte(driver), 0o700))
	fixture := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	registry := materialIndex(t, fixture)
	material := obj(array(registry["materials"])[0])
	id := str(material["id"])
	core := filepath.Join(fixture, "packages", id, "material")
	plan := readJSON(t, filepath.Join(fixture, "source", "plan.json"))
	plan["project"], plan["recordingIsolation"] = "/synthetic/legacy-project", "personal-skills-v1"
	require.NoError(t, os.RemoveAll(core))
	writeJSON(t, filepath.Join(core, "plan.json"), plan)
	raw := []byte("{\"ordinal\":1,\"direction\":\"c2s\",\"channel\":\"protocol\",\"epoch\":1,\"elapsedMs\":0,\"wire\":{\"id\":1,\"method\":\"ping\"}}\n{\"ordinal\":2,\"direction\":\"s2c\",\"channel\":\"protocol\",\"epoch\":1,\"elapsedMs\":1,\"wire\":{\"id\":1,\"result\":{}}}\n")
	require.NoError(t, os.MkdirAll(filepath.Join(core, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(core, "raw", "raw.jsonl"), raw, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(core, "raw", "stderr.raw"), nil, 0o600))
	writeJSON(t, filepath.Join(core, "raw", "transport.json"), object{"captureStatus": "complete", "cleanupComplete": true, "nativeLaunched": true, "reaped": true, "clientEOF": true, "frames": 2})
	writeJSON(t, filepath.Join(core, "raw", "manifest.json"), object{
		"captureStatus": "complete", "files": officialInventory(t, filepath.Join(core, "raw")),
		"native":          object{"executable": "/synthetic/native", "sha256": strings.Repeat("a", 64), "cwd": plan["project"]},
		"recordingFormat": object{"name": "agent-mock-jsonl", "version": 1, "transport": "stdio-jsonl"},
	})
	writeJSON(t, filepath.Join(core, "report.json"), object{"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"], "status": "passed", "branchStatus": "passed", "captureStatus": "complete", "officialSDK": object{"package": "openai-codex", "version": "synthetic-legacy-contract"}})
	writeJSON(t, filepath.Join(core, "result.json"), object{"status": "passed"})
	writeJSON(t, filepath.Join(core, "summary.json"), object{"provider": plan["provider"], "case": plan["caseId"], "mode": "record", "result": object{"status": "passed"}, "recordingSHA256": digestFile(t, filepath.Join(core, "raw", "raw.jsonl"))})
	writeJSON(t, filepath.Join(core, "manifest.json"), object{"provenance": "capture", "status": "passed", "captureStatus": "complete", "credentialScan": "pass", "planSHA256": digestFile(t, filepath.Join(core, "plan.json")), "files": officialInventory(t, filepath.Join(core, "raw"))})
	files := officialInventory(t, core)
	material["formatVersion"], material["kind"], material["action"], material["provenance"] = 2, "capture", "record", "official"
	material["files"], material["connections"] = files, []string{"main"}
	material["planSHA256"], material["manifestSHA256"], material["recordingSHA256"] = files["plan.json"], files["manifest.json"], files["raw/raw.jsonl"]
	for _, key := range []string{"officialOrigin", "originSHA256", "sourcePlanSHA256", "legacyInventorySHA256"} {
		delete(material, key)
	}
	delete(registry, "descriptorless")
	writeJSON(t, filepath.Join(core, "material.json"), material)
	writeJSON(t, filepath.Join(fixture, "packages", "index.json"), registry)
	current := readJSON(t, filepath.Join(fixture, "current.json"))
	current["materialSHA256"], current["manifestSHA256"] = digestFile(t, filepath.Join(core, "material.json")), files["manifest.json"]
	delete(current, "legacyFormat")
	delete(current, "legacyInventorySHA256")
	writeJSON(t, filepath.Join(fixture, "current.json"), current)
	before := officialInventory(t, core)
	corpus := newSyntheticCorpus(t)
	result := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixture, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	require.Zero(t, result.code, result.stderr)
	verifyRoot, verification := latestOfficialVerification(t, fixture)
	require.Equal(t, "passed", verification["status"])
	require.EqualValues(t, 3, verification["schemaVersion"])
	admission := readJSON(t, filepath.Join(fixture, "runtime", "official", filepath.Base(verifyRoot), "admission-summary.json"))
	require.Equal(t, "admitted", admission["status"])
	attempt := str(admission["materials"])
	require.Equal(t, before, officialInventory(t, filepath.Join(attempt, "material")))
	require.Equal(t, before, officialInventory(t, core))
	require.FileExists(t, filepath.Join(attempt, "verify", filepath.Base(verifyRoot), "verification.json"))
	imported := prepareFixture(t, env, "--recording", attempt)
	require.Equal(t, before, officialInventory(t, filepath.Join(imported, "packages", id, "material")))
}
