package corpus

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Public mock verification has no verify execution or materialization overlay.
// Its portable evidence is the exact ordinary execution and receipts, copied
// byte-for-byte after the supervised processes have exited.
func (v *Validation) publicVerification(source, root string, plan tool.Object) bool {
	var evidence tool.Object
	if tool.ReadJSON(filepath.Join(root, "verification.json"), &evidence) != nil || evidence["pipeline"] != "public-agent-mock" {
		return false
	}
	schemaVersion := evidence["schemaVersion"]
	v.Check(schemaVersion == float64(1) || schemaVersion == float64(2), "unsupported official verification schemaVersion")
	v.Check(evidence["status"] == "passed" && evidence["error"] == nil && evidence["cleanupComplete"] == true, "official verification failed or incomplete")
	v.Check(evidence["case"] == plan["caseId"] && evidence["provider"] == plan["provider"], "official verification binding differs")
	actualSource := v.Inventory(source, false)
	expectedSource := tool.Obj(evidence["sourceFiles"])
	if actualSource["material.json"] == nil && expectedSource["material.json"] != nil {
		// Adopted regression views omit the fixture-local material descriptor.
		// Every published byte must still match the original verified closure.
		selected := tool.Object{}
		for name := range v.Required(source, plan) {
			selected[name] = expectedSource[name]
		}
		expectedSource = selected
	}
	v.Check(reflect.DeepEqual(actualSource, expectedSource), "verification source inventory differs")
	v.Hashes(root, tool.Obj(evidence["files"]))
	requiredDriverEvidence := []string{"driver/replay-report.json", "driver/result.json", "driver/request.json", "driver/materials/case.json"}
	if schemaVersion == float64(2) {
		requiredDriverEvidence = append(requiredDriverEvidence, "driver/runtime.json", "driver/mock-isolation.json")
	}
	for _, name := range requiredDriverEvidence {
		v.Check(tool.Str(tool.Obj(evidence["files"])[name]) != "", "verification omits driver evidence hash")
	}

	driver := tool.Obj(evidence["driverProcess"])
	v.Check(driver["started"] == true && driver["reaped"] == true && driver["exitCode"] == float64(0) && driver["cleanupComplete"] == true, "official driver did not exit successfully")
	business := v.Read(root, "driver/replay-report.json")
	v.Check(business["status"] == "passed" && business["branchStatus"] == "passed" && business["captureStatus"] == "complete" && v.Read(root, "driver/result.json")["status"] == "passed", "official business did not pass")
	refs := tool.Array(evidence["connections"])
	v.Check(len(refs) == len(Phases(plan)), "verification connection inventory differs")
	seen := map[string]bool{}
	executionID := ""
	executions := map[string]tool.Object{}
	for _, item := range refs {
		ref := tool.Obj(item)
		id := tool.Str(ref["connectionId"])
		v.Check((id == "main" || (id == "probe" && plan["driverProfile"] == "remote-close")) && !seen[id], "invalid or duplicate verified connection")
		if !Local(id) {
			continue
		}
		seen[id] = true
		prefix := "driver/connections/" + id + "/"
		files := tool.Obj(ref["evidenceFiles"])
		for _, name := range []string{"execution.json", "result.json", "replay.json", "process.json"} {
			v.Check(files[prefix+name] != nil && files[prefix+name] == tool.Obj(evidence["files"])[prefix+name], "verification omits connection evidence hash")
		}
		v.Hashes(root, files)
		execution := v.Read(root, prefix+"execution.json")
		executions[tool.Str(execution["executionId"])] = execution
		result := v.Read(root, prefix+"result.json")
		process := v.Read(root, prefix+"process.json")
		receipt := v.Read(root, prefix+"replay.json")
		peer := tool.Obj(ref["process"])
		v.Check(reflect.DeepEqual(peer, v.Read(root, "driver/peer-"+id+".process.json")), "launcher process differs from verification reference")
		v.Check(tool.Obj(evidence["priorExecutions"])[tool.Str(ref["executionId"])] != true, "verification borrows a previous execution")

		material := tool.Obj(execution["artifact"])
		v.Check(reflect.DeepEqual(material, evidence["material"]), "mock execution material differs")
		v.Check(execution["command"] == "mock" && evidenceFixtureID(execution) == evidenceFixtureID(evidence) && execution["executionId"] == ref["executionId"], "mock execution reference differs")
		if executionID == "" {
			executionID = tool.Str(execution["executionId"])
		}
		v.Check(executionID != "" && executionID == execution["executionId"], "verified connections disagree on execution")
		v.Check(ref["executionSHA256"] == files[prefix+"execution.json"] && reflect.DeepEqual(ref["finished"], result), "mock notification differs from durable state")
		v.Check(result["processId"] == peer["pid"] && result["status"] == "passed" && peer["exitCode"] == float64(0) && peer["reaped"] == true && peer["cleanupComplete"] == true, "mock process was not supervised and reaped")
		v.Check(evidenceFixtureID(process) == evidenceFixtureID(execution) && process["executionId"] == execution["executionId"] && process["connectionId"] == id && process["status"] == "passed" && process["exitCode"] == float64(0), "mock process receipt differs")
		raw := "raw/raw.jsonl"
		if id == "probe" {
			raw = "remote-probe/" + raw
		}

		data, err := os.ReadFile(filepath.Join(source, raw))
		if err != nil {
			v.Check(false, "verified raw stream is missing")
		} else {
			v.Check(len(data) > 0 && receipt["frames"] == float64(len(bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")))), "verified frame count differs")
		}
		v.Check(receipt["recordingSHA256"] == v.Digest(source, raw) && receipt["planSHA256"] == v.Digest(source, "plan.json") && receipt["provider"] == plan["provider"] && receipt["status"] == "passed" && receipt["fullConsumption"] == true && receipt["clientEOF"] == true && receipt["cleanupComplete"] == true && receipt["nativeLaunched"] == false && receipt["exitCode"] == float64(0) && receipt["error"] == nil, "mock receipt does not prove exact complete replay")
	}
	if schemaVersion == float64(2) {
		v.validateMockIsolation(root, plan, evidence, executions)
	}
	return true
}

func (v *Validation) validateMockIsolation(root string, plan, evidence tool.Object, executions map[string]tool.Object) {
	isolation := v.Read(root, "driver/mock-isolation.json")
	reference := tool.Obj(evidence["mockIsolation"])
	executionID := tool.Str(isolation["executionId"])
	execution := executions[executionID]
	v.Check(isolation["schemaVersion"] == float64(2) && isolation["fixtureId"] == evidence["fixtureId"] && executionID != "" && len(execution) > 0, "current mock isolation identity is missing or differs")
	v.Check(reference["path"] == "driver/mock-isolation.json" && reference["sha256"] == tool.Obj(evidence["files"])["driver/mock-isolation.json"], "current mock isolation reference differs")
	v.Check(isolation["command"] == "mock" && reflect.DeepEqual(isolation["material"], evidence["material"]), "current mock isolation belongs to another material or action")
	v.Check(reflect.DeepEqual(isolation["nativeArgs"], execution["nativeArgs"]), "current mock isolation argv differs from its execution")
	executionReference := tool.Obj(isolation["execution"])
	v.Check(executionReference["path"] == "executions/"+executionID+"/execution.json" && executionReference["sha256"] == tool.Obj(evidence["files"])["driver/connections/main/execution.json"], "current mock isolation execution reference differs")
	workspace := tool.Str(isolation["workspace"])
	overlay := filepath.Dir(workspace)
	v.Check(filepath.IsAbs(workspace) && filepath.Clean(workspace) == workspace && filepath.Base(workspace) == "workspace" && filepath.Base(overlay) == executionID && filepath.Base(filepath.Dir(overlay)) == "executions", "current mock workspace is outside its execution overlay")
	v.Check(isolation["protocolCwd"] != nil, "current mock protocol cwd is missing")
	if plan["provider"] == "codex" {
		v.Check(isolation["protocolCwd"] == plan["project"], "codex mock protocol identity was replaced")
	}
	env := tool.Obj(isolation["environment"])
	for key, relative := range map[string]string{"HOME": "home", "TMPDIR": "tmp", "XDG_CONFIG_HOME": "home/.config", "XDG_CACHE_HOME": "home/.cache", "XDG_DATA_HOME": "home/.local/share"} {
		v.Check(env[key] == filepath.Join(overlay, relative), "current mock environment escapes isolation: "+key)
	}
	configKey := map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[tool.Str(plan["provider"])]
	v.Check(configKey != "" && env[configKey] == filepath.Join(overlay, "config"), "current mock native config escapes isolation")
	v.Check(isolation["home"] == filepath.Join(overlay, "home") && isolation["config"] == filepath.Join(overlay, "config") && isolation["tmp"] == filepath.Join(overlay, "tmp"), "current mock directory relationships differ")
}
