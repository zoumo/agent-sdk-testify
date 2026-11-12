package corpus

import (
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func publicCapture(source string) bool {
	_, err := os.Lstat(filepath.Join(source, "official-origin.json"))
	return !os.IsNotExist(err)
}

// publicCaptureBusiness validates portable facts from the original ordinary
// record execution and its settled official parent. Captured material remains
// immutable: the parent's later report is read from the verification bundle.
func (v *Validation) publicCaptureBusiness(source, replay string, plan tool.Object) (tool.Object, tool.Object) {
	const prefix = "driver/record-origin/"
	verification := v.Read(replay, "verification.json")
	reference := tool.Obj(verification["officialRecord"])
	files := tool.Obj(reference["files"])
	v.Check(reference["root"] == "driver/record-origin" && len(files) > 0, "official record qualification bundle is missing")
	actual := v.Inventory(filepath.Join(replay, "driver", "record-origin"), false)
	v.Check(reflect.DeepEqual(actual, files), "official record qualification bundle changed")
	for name, digest := range files {
		v.Check(tool.Obj(verification["files"])[prefix+name] == digest, "verification does not hash original record evidence")
	}
	origin := v.Read(source, "official-origin.json")
	originID := evidenceFixtureID(origin)
	registered := v.Read(replay, prefix+"run/origin.json")
	v.Check(reflect.DeepEqual(origin, registered), "original official record origin differs")
	v.Check(origin["sourceSHA256"] == v.Digest(source, "source-plan.json") && plan["sourcePlanSHA256"] == origin["sourceSHA256"], "official source plan binding differs")
	manifest := v.Read(source, "manifest.json")
	v.Check(manifest["sourcePlanSHA256"] == origin["sourceSHA256"] && origin["caseId"] == plan["caseId"] && origin["agentType"] == plan["provider"], "official source identity differs")
	run := v.Read(replay, prefix+"run/result.json")
	fixture := v.readFixtureEvidence(replay, prefix)
	execution := v.Read(replay, prefix+"execution.json")
	material := tool.Obj(execution["artifact"])
	v.Check(run["id"] == origin["runId"] && evidenceFixtureID(run) == originID && run["status"] == "passed" && run["credentialScan"] == "pass" && run["cleanupComplete"] == true && run["error"] == nil, "official recording business did not settle successfully")
	driver := tool.Obj(run["driverProcess"])
	v.Check(driver["started"] == true && driver["reaped"] == true && driver["exitCode"] == float64(0) && driver["cleanupComplete"] == true, "official recording driver did not exit cleanly")
	v.Check(tool.Obj(run["driver"])["sha256"] == origin["driverSHA256"], "original driver identity differs")
	driverFiles := v.Inventory(filepath.Join(replay, filepath.FromSlash(prefix), "run", "driver"), false)
	v.Check(reflect.DeepEqual(driverFiles, run["driverFiles"]) && reflect.DeepEqual(driverFiles, v.Read(replay, prefix+"run/driver-files.json")), "original driver file closure differs")
	business := v.Read(replay, prefix+"run/driver/report.json")
	outcome := v.Read(replay, prefix+"run/driver/result.json")
	v.Check(reflect.DeepEqual(business, run["report"]) && reflect.DeepEqual(outcome, run["business"]) && outcome["status"] == "passed", "original business report differs from settled orchestration")
	v.Check(tool.Obj(fixture["source"])["kind"] != "plan" && tool.Obj(fixture["source"])["provenance"] != "consumer", "consumer source has no official qualification")
	v.Check(evidenceFixtureID(fixture) == originID && tool.Obj(fixture["source"])["sha256"] == origin["recordSourceSHA256"] && fixture["agentType"] == plan["provider"], "original fixture source differs")
	v.Check(execution["command"] == "record" && evidenceFixtureID(execution) == evidenceFixtureID(fixture) && reflect.DeepEqual(execution["source"], fixture["source"]) && reflect.DeepEqual(material["officialOrigin"], origin), "original record execution identity differs")
	inventory := v.Inventory(source, false)
	delete(inventory, "material.json")
	v.Check(reflect.DeepEqual(inventory, material["files"]) && material["provenance"] == "official" && material["planSHA256"] == v.Digest(source, "plan.json") && material["manifestSHA256"] == v.Digest(source, "manifest.json") && material["recordingSHA256"] == v.Digest(source, "raw/raw.jsonl"), "original capture closure differs")
	ids := tool.Strings(origin["connections"])
	refs := tool.Array(run["connections"])
	v.Check(len(ids) == len(Phases(plan)) && len(refs) == len(ids) && len(tool.Array(execution["connections"])) == len(ids), "original record connection inventory differs")
	v.Check(reflect.DeepEqual(material["connections"], origin["connections"]), "sealed source connections differ from original capture")
	seen := map[string]bool{}
	for _, id := range ids {
		v.Check((id == "main" || (id == "probe" && plan["driverProfile"] == "remote-close")) && !seen[id], "invalid or duplicate original source connection")
		seen[id] = true
		if !Local(id) {
			v.Check(false, "invalid original connection")
			continue
		}
		proof := v.Read(source, "origin/"+id+".json")
		durable := v.Read(replay, prefix+"results/"+id+".json")
		found := false
		for _, item := range refs {
			ref := tool.Obj(item)
			if ref["connectionId"] != id {
				continue
			}
			v.Check(!found, "duplicate original connection reference")
			found = true
			started, finished, process := tool.Obj(ref["started"]), tool.Obj(ref["finished"]), tool.Obj(ref["process"])
			v.Check(ref["executionId"] == execution["executionId"] && ref["executionSHA256"] == v.Digest(replay, prefix+"execution.json") && reflect.DeepEqual(ref["material"], material), "original connection capture reference differs")
			v.Check(reflect.DeepEqual(proof["origin"], origin) && proof["executionId"] == execution["executionId"] && proof["connectionId"] == id && proof["toolSHA256"] == fixtureToolSHA256(fixture) && proof["protocolPid"] == process["pid"] && proof["supervisorPid"] == process["supervisorPid"], "original native launch proof differs")
			v.Check(process["started"] == true && process["reaped"] == true && process["exitCode"] == float64(0) && process["cleanupComplete"] == true && process["error"] == nil, "original public record process failed")
			v.Check(reflect.DeepEqual(process, v.Read(replay, prefix+"run/driver/peer-"+id+".process.json")), "original launcher process differs")
			v.Check(reflect.DeepEqual(finished, durable) && started["status"] == "running" && finished["status"] == "passed", "original record notification differs from durable result")
			for _, notice := range []tool.Object{started, finished} {
				v.Check(notice["processId"] == process["pid"] && evidenceFixtureID(notice) == evidenceFixtureID(fixture) && notice["executionId"] == execution["executionId"] && notice["connectionId"] == id && notice["command"] == "record", "original record notification identity differs")
			}
		}
		v.Check(found, "original recording omits required connection")
	}
	preparation := tool.Obj(run["recordPreparation"])
	switch preparation["schemaVersion"] {
	case float64(1):
		v.Check(preparation["executionId"] == execution["executionId"] && preparation["isolationSHA256"] == v.Digest(replay, prefix+"isolation.json") && preparation["preparedPlanSHA256"] == v.Digest(replay, prefix+"prepared-plan.json"), "original native preparation hash binding differs")
	case float64(2):
		v.Check(preparation["executionId"] == execution["executionId"] && reflect.DeepEqual(preparation["material"], material), "original native preparation identity differs")
		for name, file := range map[string]string{
			"execution": "execution.json", "isolation": "isolation.json",
			"preparedPlan": "prepared-plan.json", "materialPlan": "material-plan.json",
		} {
			reference := tool.Obj(preparation[name])
			v.Check(reference["path"] != nil && reference["sha256"] == v.Digest(replay, prefix+file), "original native preparation reference differs: "+name)
		}
		v.Check(tool.Obj(preparation["materialPlan"])["sha256"] == v.Digest(source, "plan.json"), "original native material plan differs from sealed plan")
	default:
		v.Check(false, "unsupported official record preparation schemaVersion")
	}
	v.captureIsolation(replay, prefix, plan, fixture, execution)
	return business, execution
}

func (v *Validation) captureIsolation(replay, prefix string, plan, fixture, execution tool.Object) {
	receipt := v.Read(replay, prefix+"isolation.json")
	prepared := v.Read(replay, prefix+"prepared-plan.json")
	switch receipt["schemaVersion"] {
	case float64(1):
		v.Check(receipt["method"] == "native-preparation" && receipt["policy"] == "personal-skills-v1" && evidenceFixtureID(receipt) == evidenceFixtureID(fixture) && receipt["executionId"] == execution["executionId"] && receipt["toolSHA256"] == fixtureToolSHA256(fixture), "recording-time native isolation evidence is missing or differs")
		v.Check(receipt["preparedPlanSHA256"] == v.Digest(replay, prefix+"prepared-plan.json"), "prepared isolation plan hash differs")
		derived := tool.Object{}
		for key, value := range plan {
			if key != "sourcePlanSHA256" {
				derived[key] = value
			}
		}
		v.Check(reflect.DeepEqual(prepared, derived), "sealed plan differs from actual isolated preparation")
		v.Check(receipt["nativeSHA256"] == plan["cliSHA256"] && reflect.DeepEqual(receipt["args"], plan["args"]), "native isolation invocation differs from sealed plan")
	case float64(2):
		materialPlan := v.Read(replay, prefix+"material-plan.json")
		executionID := tool.Str(execution["executionId"])
		executionReference := tool.Obj(receipt["execution"])
		preparedReference := tool.Obj(receipt["preparedPlan"])
		materialReference := tool.Obj(receipt["materialPlan"])
		native := tool.Obj(receipt["native"])
		v.Check(receipt["method"] == "native-preparation" && receipt["policy"] == "personal-skills-v1" && evidenceFixtureID(receipt) == evidenceFixtureID(fixture) && receipt["executionId"] == executionID, "recording-time native isolation evidence is missing or differs")
		v.Check(executionReference["path"] == "executions/"+executionID+"/execution.json" && executionReference["sha256"] == v.Digest(replay, prefix+"execution.json"), "recording isolation execution reference differs")
		v.Check(preparedReference["path"] == "runtime/executions/"+executionID+"/record-plan.json" && preparedReference["sha256"] == v.Digest(replay, prefix+"prepared-plan.json"), "recording isolation prepared-plan reference differs")
		v.Check(materialReference["path"] == "runtime/executions/"+executionID+"/material-plan.json" && materialReference["sha256"] == v.Digest(replay, prefix+"material-plan.json"), "recording isolation material-plan reference differs")
		v.Check(reflect.DeepEqual(materialPlan, plan) && reflect.DeepEqual(MaterialPlanFromRuntime(prepared), materialPlan), "sealed plan differs from its isolated runtime plan")
		v.Check(native["sha256"] == plan["cliSHA256"] && reflect.DeepEqual(native["args"], plan["args"]), "native isolation invocation differs from sealed plan")
	default:
		v.Check(false, "unsupported recording isolation schemaVersion")
	}
	// The recorded execution descriptor no longer persists machine-local paths.
	// Bind the observed preparation to the fixed overlay layout instead.
	overlay := tool.Str(prepared["root"])
	v.Check(filepath.IsAbs(overlay) && filepath.Clean(overlay) == overlay, "record isolation root is not an absolute clean path")
	v.Check(filepath.Base(overlay) == tool.Str(execution["executionId"]) && filepath.Base(filepath.Dir(overlay)) == "executions", "record isolation root is not the fixed execution overlay")
	workspace := filepath.Join(overlay, "workspace")
	v.recordWorkspace = workspace
	for key, relative := range map[string]string{"workspace": "workspace", "home": "home", "config": "config", "tmp": "tmp", "historySeed": "history-seed"} {
		if value, ok := execution[key]; ok {
			v.Check(value == filepath.Join(overlay, relative), "record execution isolation path differs: "+key)
		}
	}
	env := tool.Obj(receipt["environment"])
	for key, relative := range map[string]string{"HOME": "home", "TMPDIR": "tmp", "XDG_CONFIG_HOME": "home/.config", "XDG_CACHE_HOME": "home/.cache", "XDG_DATA_HOME": "home/.local/share"} {
		v.Check(env[key] == filepath.Join(overlay, relative), "actual record environment escapes isolation: "+key)
	}
	configKey := map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[tool.Str(plan["provider"])]
	v.Check(configKey != "" && env[configKey] == filepath.Join(overlay, "config"), "actual native config escapes isolation")
	if recorded, ok := execution["env"]; ok {
		executionEnv := tool.Obj(recorded)
		for key := range env {
			v.Check(env[key] == executionEnv[key], "actual record environment differs from its execution: "+key)
		}
	}
	if protocolCwd, ok := execution["protocolCwd"]; ok {
		v.Check(protocolCwd == workspace, "record protocol cwd differs from its workspace")
	}
	v.Check(receipt["cwd"] == workspace && prepared["home"] == filepath.Join(overlay, "home") && prepared["config"] == filepath.Join(overlay, "config"), "recording cwd and isolation root differ")
	if fixture["schemaVersion"] == float64(6) {
		runtimeRoot := filepath.Dir(filepath.Dir(overlay))
		project := filepath.Join(runtimeRoot, "routes", "record", "workspace")
		v.Check(prepared["project"] == project && prepared["_fixtureProject"] == workspace, "recording project binding differs from its fixed route")
	} else {
		v.Check(prepared["project"] == tool.Obj(tool.Obj(fixture["routes"])["record"])["workspace"], "recording project binding differs from its route")
	}
}

func fixtureToolSHA256(fixture tool.Object) any {
	switch fixture["schemaVersion"] {
	case float64(5), float64(6):
		return tool.Obj(fixture["tool"])["sha256"]
	case float64(1), float64(2), float64(3):
		return fixture["toolSHA256"]
	default:
		return nil
	}
}
