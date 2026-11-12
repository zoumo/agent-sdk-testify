package corpus

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func packageEnvelope(root, kind, id string, material Material) (tool.Object, error) {
	if !packageID(id) {
		return nil, errors.New("invalid official domain identity")
	}
	if err := validatePackageDomain(root, kind, id, material); err != nil {
		return nil, err
	}
	name := "result.json"
	if kind == "verify" {
		name = "verification.json"
	}
	var result tool.Object
	if err := tool.ReadJSON(filepath.Join(root, kind, id, name), &result); err != nil {
		return nil, err
	}
	if result["status"] != "passed" || result["cleanupComplete"] != true || result["error"] != nil {
		return nil, errors.New("official evidence did not pass and settle")
	}
	return result, nil
}

func validatePackageBusiness(root, kind, id string) error {
	name := "report.json"
	if kind == "verify" {
		name = "replay-report.json"
	}
	var report, outcome, plan tool.Object
	for path, target := range map[string]*tool.Object{
		filepath.Join(root, kind, id, name):                 &report,
		filepath.Join(root, kind, id, "driver-result.json"): &outcome,
		filepath.Join(root, "material", "plan.json"):        &plan,
	} {
		if err := tool.ReadJSON(path, target); err != nil {
			return err
		}
	}
	story := plan["storyId"]
	if story == nil {
		story = plan["story"]
	}
	sdk := tool.Obj(report["officialSDK"])
	if report["status"] != "passed" || report["branchStatus"] != "passed" || report["captureStatus"] != "complete" || len(tool.Array(report["gaps"])) != 0 || report["provider"] != plan["provider"] || report["caseId"] != plan["caseId"] || report["storyId"] != story || outcome["status"] != "passed" || sdk["package"] != map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[tool.Str(plan["provider"])] || tool.Str(sdk["version"]) == "" {
		return errors.New("official SDK business evidence is absent, failed or untriggered")
	}
	return nil
}

// validateOfficialExecutions follows references to the ordinary sealed execution
// domains. It neither snapshots those domains nor accepts an earlier mock run.
func validateOfficialExecutions(root, kind, id string, material Material, result tool.Object) error {
	refs := tool.Array(result["executions"])
	if len(refs) != 1 {
		return errors.New("official evidence must bind its one actual execution")
	}
	ref := tool.Obj(refs[0])
	executionID := tool.Str(ref["executionId"])
	if !packageID(executionID) || ref["path"] != "executions/"+executionID+"/result.json" {
		return errors.New("invalid official execution reference")
	}
	if err := VerifyFile(root, tool.Str(ref["path"]), tool.Str(ref["sha256"])); err != nil {
		return err
	}
	if err := validatePackageDomain(root, "executions", executionID, material); err != nil {
		return err
	}
	var aggregate, declaration tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "executions", executionID, "result.json"), &aggregate); err != nil {
		return err
	}
	if err := tool.ReadJSON(filepath.Join(root, "executions", executionID, "execution.json"), &declaration); err != nil {
		return err
	}
	action := "record"
	if kind == "verify" {
		action = "mock"
	}
	if aggregate["status"] != "passed" || declaration["command"] != action || declaration["fixtureId"] != result["fixtureId"] || aggregate["fixtureId"] != result["fixtureId"] || aggregate["executionSHA256"] != ref["executionSHA256"] || !reflect.DeepEqual(ref["connections"], aggregate["connections"]) {
		return errors.New("official execution identity, status or connections differ")
	}
	if kind == "record" && (result["executionId"] != executionID || aggregate["publicationCommitted"] != true) {
		return errors.New("official record did not commit its execution")
	}
	connections := tool.Array(declaration["connections"])
	if len(connections) != len(material.Connections) {
		return errors.New("official execution connection closure differs")
	}
	seen := map[string]bool{}
	for _, item := range connections {
		connectionID := tool.Str(tool.Obj(item)["id"])
		if !packageID(connectionID) || seen[connectionID] {
			return errors.New("invalid official connection")
		}
		seen[connectionID] = true
		var peer, durable tool.Object
		if err := tool.ReadJSON(filepath.Join(root, kind, id, "peers", connectionID+".json"), &peer); err != nil {
			return err
		}
		if err := tool.ReadJSON(filepath.Join(root, "executions", executionID, "receipts", connectionID+".result.json"), &durable); err != nil {
			return err
		}
		running := tool.Obj(peer["running"])
		final := tool.Obj(peer["result"])
		expected := "executions/" + executionID + "/receipts/" + connectionID + ".result.json"
		if peer["schemaVersion"] != float64(1) || peer["started"] != true || peer["reaped"] != true || peer["cleanupComplete"] != true || peer["exitCode"] != float64(0) || peer["error"] != nil || final["path"] != expected {
			return errors.New("official peer was not supervised and reaped")
		}
		if err := VerifyFile(root, expected, tool.Str(final["sha256"])); err != nil {
			return err
		}
		for _, notice := range []tool.Object{running, durable} {
			if notice["schemaVersion"] != float64(3) || notice["fixtureId"] != result["fixtureId"] || notice["materialId"] != material.ID || notice["executionId"] != executionID || notice["connectionId"] != connectionID || notice["processId"] != peer["pid"] || notice["command"] != action || notice["executionSHA256"] != ref["executionSHA256"] || notice["execution"] != "executions/"+executionID+"/execution.json" || notice["result"] != expected {
				return errors.New("official notification identity differs")
			}
		}
		if running["status"] != "running" || durable["status"] != "passed" {
			return errors.New("official notification did not complete")
		}
		if kind == "record" {
			var proof tool.Object
			if err := tool.ReadJSON(filepath.Join(root, "material", "origin", connectionID+".json"), &proof); err != nil {
				return err
			}
			if proof["schemaVersion"] != float64(2) || proof["originSHA256"] != material.OriginSHA256 || proof["runId"] != id || proof["executionId"] != executionID || proof["connectionId"] != connectionID || proof["protocolPid"] != peer["pid"] || proof["supervisorPid"] != peer["supervisorPid"] {
				return errors.New("official origin launch proof differs")
			}
		} else {
			if err := validatePackageMockReceipt(root, executionID, connectionID, material); err != nil {
				return err
			}
		}
	}
	for _, connectionID := range material.Connections {
		if !seen[connectionID] {
			return errors.New("official execution omits declared connection")
		}
	}
	if kind == "verify" {
		return validatePackageMockIsolation(root, executionID, material, declaration)
	}
	return nil
}

func validatePackageMockReceipt(root, executionID, connectionID string, material Material) error {
	var receipt, process tool.Object
	dir := filepath.Join(root, "executions", executionID, "receipts")
	if err := tool.ReadJSON(filepath.Join(dir, connectionID+".json"), &receipt); err != nil {
		return err
	}
	if err := tool.ReadJSON(filepath.Join(dir, connectionID+".process.json"), &process); err != nil {
		return err
	}
	raw := "raw/raw.jsonl"
	if connectionID == "probe" {
		raw = "remote-probe/" + raw
	}
	data, err := os.ReadFile(filepath.Join(root, "material", filepath.FromSlash(raw)))
	if err != nil {
		return err
	}
	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "material", "plan.json"), &plan); err != nil {
		return err
	}
	frames := len(bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}))
	if len(data) == 0 || receipt["schemaVersion"] != float64(2) || receipt["version"] != float64(1) || receipt["recordingSHA256"] != tool.DigestBytes(data) || receipt["recording"] != "material" || receipt["provider"] != plan["provider"] || receipt["frames"] != float64(frames) || receipt["planSHA256"] != material.PlanSHA256 || receipt["status"] != "passed" || receipt["fullConsumption"] != true || receipt["clientEOF"] != true || receipt["cleanupComplete"] != true || receipt["nativeLaunched"] != false || receipt["exitCode"] != float64(0) || receipt["error"] != nil || process["schemaVersion"] != float64(1) || process["executionId"] != executionID || process["connectionId"] != connectionID || process["status"] != "passed" || process["exitCode"] != float64(0) {
		return errors.New("mock receipt does not prove complete ordinary replay")
	}
	return nil
}

func validatePackageMockIsolation(root, executionID string, material Material, declaration tool.Object) error {
	var isolation, plan tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "executions", executionID, "isolation.json"), &isolation); err != nil {
		return err
	}
	if err := tool.ReadJSON(filepath.Join(root, "material", "plan.json"), &plan); err != nil {
		return err
	}

	var prepared tool.Object
	preparedPath := "executions/" + executionID + "/prepared-plan.json"
	preparedRef := tool.Obj(isolation["preparedPlan"])
	if isolation["method"] != "mock-preparation" || preparedRef["path"] != preparedPath {
		return errors.New("mock prepared-plan reference differs")
	}
	if err := VerifyFile(root, preparedPath, tool.Str(preparedRef["sha256"])); err != nil {
		return err
	}
	if err := tool.ReadJSON(filepath.Join(root, filepath.FromSlash(preparedPath)), &prepared); err != nil {
		return err
	}
	if prepared["schemaVersion"] != float64(3) || prepared["fixtureId"] != declaration["fixtureId"] || prepared["executionId"] != executionID || prepared["materialId"] != material.ID || prepared["command"] != "mock" || prepared["provider"] != plan["provider"] || prepared["sourcePlanSHA256"] != material.SourcePlanSHA256 || prepared["planSHA256"] != material.PlanSHA256 || prepared["manifestSHA256"] != material.ManifestSHA256 || !reflect.DeepEqual(prepared["args"], declaration["nativeArgs"]) || !reflect.DeepEqual(prepared["limits"], declaration["limits"]) {
		return errors.New("mock prepared-plan identity differs")
	}
	for key, isolationKey := range map[string]string{"project": "protocolCwd", "home": "home", "config": "config", "tmp": "tmp"} {
		if prepared[key] != isolation[isolationKey] {
			return errors.New("mock prepared-plan environment differs")
		}
	}
	if prepared["root"] != filepath.Dir(tool.Str(isolation["workspace"])) {
		return errors.New("mock prepared-plan overlay differs")
	}
	for _, name := range []string{"caseId", "storyId", "driverProfile", "model", "reasoningEffort"} {
		if !reflect.DeepEqual(prepared[name], plan[name]) {
			return errors.New("mock prepared-plan selection differs")
		}
	}
	ref := tool.Obj(isolation["execution"])
	path := "executions/" + executionID + "/execution.json"
	if isolation["schemaVersion"] != float64(3) || isolation["fixtureId"] != declaration["fixtureId"] || isolation["executionId"] != executionID || isolation["materialId"] != material.ID || isolation["command"] != "mock" || !reflect.DeepEqual(isolation["nativeArgs"], declaration["nativeArgs"]) || ref["path"] != path {
		return errors.New("current mock isolation identity differs")
	}
	if err := VerifyFile(root, path, tool.Str(ref["sha256"])); err != nil {
		return err
	}
	workspace := tool.Str(isolation["workspace"])
	overlay := filepath.Dir(workspace)
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace || filepath.Base(workspace) != "workspace" || filepath.Base(overlay) != executionID || filepath.Base(filepath.Dir(overlay)) != "executions" || isolation["protocolCwd"] == nil {
		return errors.New("current mock workspace or protocol identity differs")
	}
	if plan["provider"] == "codex" && isolation["protocolCwd"] != plan["project"] {
		return errors.New("current mock protocol cwd differs")
	}
	for _, key := range []string{"home", "config", "tmp"} {
		if isolation[key] != filepath.Join(overlay, key) {
			return errors.New("current mock directory relationships differ")
		}
	}
	if err := validateIsolationEnvironment(overlay, tool.Str(plan["provider"]), tool.Obj(isolation["environment"])); err != nil {
		return err
	}
	connections := tool.Array(isolation["connections"])
	if len(connections) != len(material.Connections) {
		return errors.New("current mock isolation omits connections")
	}
	seen := map[string]bool{}
	for _, item := range connections {
		connection := tool.Obj(item)
		id := tool.Str(connection["connectionId"])
		if !slices.Contains(material.Connections, id) || seen[id] || connection["executionId"] != executionID || connection["executionSHA256"] != ref["sha256"] {
			return errors.New("current mock isolation connection differs")
		}
		seen[id] = true
	}
	return nil
}

func validateIsolationEnvironment(overlay, provider string, env tool.Object) error {
	for key, relative := range map[string]string{"HOME": "home", "TMPDIR": "tmp", "XDG_CONFIG_HOME": "home/.config", "XDG_CACHE_HOME": "home/.cache", "XDG_DATA_HOME": "home/.local/share"} {
		if env[key] != filepath.Join(overlay, relative) {
			return errors.New("native preparation environment escapes overlay")
		}
	}
	configKey := map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[provider]
	if configKey == "" || env[configKey] != filepath.Join(overlay, "config") {
		return errors.New("native preparation config escapes overlay")
	}
	return nil
}

func validatePackageRecord(root string, material Material) error {
	if material.OfficialOrigin == nil {
		return errors.New("official capture origin is absent")
	}
	id := material.OfficialOrigin.RunID
	result, err := packageEnvelope(root, "record", id, material)
	if err != nil {
		return err
	}
	if result["fixtureId"] != material.OfficialOrigin.FixtureID || result["credentialScan"] != "pass" || tool.Obj(result["driver"])["sha256"] != material.OfficialOrigin.DriverSHA256 {
		return errors.New("official record origin or credential scan differs")
	}
	process := tool.Obj(result["driverProcess"])
	if process["started"] != true || process["reaped"] != true || process["cleanupComplete"] != true || process["exitCode"] != float64(0) {
		return errors.New("record driver did not exit cleanly")
	}
	if err := validatePackageBusiness(root, "record", id); err != nil {
		return err
	}
	if err := validateOfficialExecutions(root, "record", id, material, result); err != nil {
		return err
	}
	return validatePackagePreparation(root, tool.Str(result["executionId"]), material)
}

func validatePackagePreparation(root, executionID string, material Material) error {
	var isolation, prepared, plan tool.Object
	dir := filepath.Join(root, "executions", executionID)
	for path, target := range map[string]*tool.Object{filepath.Join(dir, "isolation.json"): &isolation, filepath.Join(dir, "prepared-plan.json"): &prepared, filepath.Join(root, "material", "plan.json"): &plan} {
		if err := tool.ReadJSON(path, target); err != nil {
			return fmt.Errorf("native preparation: %w", err)
		}
	}
	if isolation["schemaVersion"] != float64(3) || prepared["schemaVersion"] != float64(3) || isolation["method"] != "native-preparation" || isolation["policy"] != "personal-skills-v1" || isolation["executionId"] != executionID || prepared["executionId"] != executionID || isolation["materialId"] != material.ID || isolation["planSHA256"] != material.PlanSHA256 || isolation["sourcePlanSHA256"] != material.SourcePlanSHA256 || prepared["sourcePlanSHA256"] != material.SourcePlanSHA256 || isolation["manifestSHA256"] != material.ManifestSHA256 || isolation["fixtureId"] != material.OfficialOrigin.FixtureID || prepared["fixtureId"] != material.OfficialOrigin.FixtureID {
		return errors.New("native preparation identity differs")
	}
	for name, file := range map[string]string{"execution": "execution.json", "preparedPlan": "prepared-plan.json"} {
		ref := tool.Obj(isolation[name])
		path := "executions/" + executionID + "/" + file
		if ref["path"] != path {
			return errors.New("native preparation reference differs")
		}
		if err := VerifyFile(root, path, tool.Str(ref["sha256"])); err != nil {
			return err
		}
	}
	if tool.Obj(isolation["native"])["sha256"] != plan["cliSHA256"] || !reflect.DeepEqual(tool.Obj(isolation["native"])["args"], plan["args"]) || prepared["cliSHA256"] != plan["cliSHA256"] || !reflect.DeepEqual(prepared["args"], plan["args"]) {
		return errors.New("native preparation invocation differs")
	}
	overlay := tool.Str(prepared["root"])
	if !filepath.IsAbs(overlay) || filepath.Clean(overlay) != overlay || filepath.Base(overlay) != executionID || filepath.Base(filepath.Dir(overlay)) != "executions" || isolation["cwd"] != filepath.Join(overlay, "workspace") {
		return errors.New("native preparation overlay differs")
	}
	for _, key := range []string{"home", "config", "tmp"} {
		if prepared[key] != filepath.Join(overlay, key) {
			return errors.New("native preparation directories escape overlay")
		}
	}
	return validateIsolationEnvironment(overlay, tool.Str(plan["provider"]), tool.Obj(isolation["environment"]))
}

func ValidateOfficialVerification(root, id string, material Material) error {
	result, err := packageEnvelope(root, "verify", id, material)
	if err != nil {
		return err
	}
	for _, key := range []string{"report", "business", "connections", "sourceFiles", "driverFiles"} {
		if _, ok := result[key]; ok {
			return errors.New("verification duplicates an owned evidence object")
		}
	}
	ref := tool.Obj(result["material"])
	if ref["path"] != "material/material.json" || result["manifestSHA256"] != material.ManifestSHA256 {
		return errors.New("verification material reference differs")
	}
	if err := VerifyFile(root, "material/material.json", tool.Str(ref["sha256"])); err != nil {
		return err
	}
	var processResult tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "verify", id, "result.json"), &processResult); err != nil {
		return err
	}
	process := tool.Obj(processResult["driverProcess"])
	if processResult["schemaVersion"] != float64(3) || processResult["id"] != id || processResult["materialId"] != material.ID || process["started"] != true || process["reaped"] != true || process["cleanupComplete"] != true || process["exitCode"] != float64(0) {
		return errors.New("verification driver did not exit cleanly")
	}
	if err := validatePackageBusiness(root, "verify", id); err != nil {
		return err
	}
	return validateOfficialExecutions(root, "verify", id, material, result)
}
