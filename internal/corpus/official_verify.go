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
	v.Check(evidence["status"] == "passed" && evidence["error"] == nil && evidence["cleanupComplete"] == true, "official verification failed or incomplete")
	v.Check(evidence["case"] == plan["caseId"] && evidence["provider"] == plan["provider"], "official verification binding differs")
	actualSource := v.Inventory(source, false)
	expectedSource := tool.Obj(evidence["sourceFiles"])
	if actualSource["material.json"] == nil && expectedSource["material.json"] != nil {
		// Adopted regression views omit the session-local material descriptor.
		// Every published byte must still match the original verified closure.
		selected := tool.Object{}
		for name := range v.Required(source, plan) {
			selected[name] = expectedSource[name]
		}
		expectedSource = selected
	}
	v.Check(reflect.DeepEqual(actualSource, expectedSource), "verification source inventory differs")
	v.Hashes(root, tool.Obj(evidence["files"]))
	for _, name := range []string{"driver/replay-report.json", "driver/result.json", "driver/request.json", "driver/materials/case.json"} {
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
		result := v.Read(root, prefix+"result.json")
		process := v.Read(root, prefix+"process.json")
		receipt := v.Read(root, prefix+"replay.json")
		peer := tool.Obj(ref["process"])
		v.Check(reflect.DeepEqual(peer, v.Read(root, "driver/peer-"+id+".process.json")), "launcher process differs from verification reference")
		v.Check(tool.Obj(evidence["priorExecutions"])[tool.Str(ref["executionId"])] != true, "verification borrows a previous execution")

		material := tool.Obj(execution["artifact"])
		v.Check(reflect.DeepEqual(material, evidence["material"]), "mock execution material differs")
		v.Check(execution["command"] == "mock" && execution["sessionId"] == evidence["sessionId"] && execution["executionId"] == ref["executionId"], "mock execution reference differs")
		if executionID == "" {
			executionID = tool.Str(execution["executionId"])
		}
		v.Check(executionID != "" && executionID == execution["executionId"], "verified connections disagree on execution")
		v.Check(ref["executionSHA256"] == files[prefix+"execution.json"] && reflect.DeepEqual(ref["finished"], result), "mock notification differs from durable state")
		v.Check(result["processId"] == peer["pid"] && result["status"] == "passed" && peer["exitCode"] == float64(0) && peer["reaped"] == true && peer["cleanupComplete"] == true, "mock process was not supervised and reaped")
		v.Check(process["sessionId"] == execution["sessionId"] && process["executionId"] == execution["executionId"] && process["connectionId"] == id && process["status"] == "passed" && process["exitCode"] == float64(0), "mock process receipt differs")
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
	return true
}
