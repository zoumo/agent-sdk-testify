package corpus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/rawjson"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func (c *Corpus) ValidateAdmission(source, replay string, review tool.Object) (tool.Object, error) {
	if review["schemaVersion"] == float64(2) {
		return c.validatePackageAdmission(source, replay, review)
	}
	v := &Validation{}
	v.Check(review["version"] == float64(1) && review["origin"] == "real-agent", "review must attest real Agent origin")
	qualification, automatic := review["qualification"]
	if automatic {
		q := tool.Obj(qualification)
		_, err := time.Parse(time.RFC3339Nano, tool.Str(q["qualifiedAt"]))
		v.Check(q["method"] == "official-sdk" && err == nil, "unsupported qualification method or time")
	} else {
		_, err := time.Parse(time.RFC3339Nano, tool.Str(review["reviewedAt"]))
		v.Check(strings.TrimSpace(tool.Str(review["reviewer"])) != "" && err == nil, "reviewer and review time required")
		for _, field := range []string{"sourceReviewed", "contentReviewed", "credentialsReviewed"} {
			v.Check(review[field] == true, "explicit review required: "+field)
		}
	}
	v.Check(reflect.DeepEqual(v.Inventory(source, false), review["captureFiles"]), "capture inventory drifted or is incomplete")
	v.Hashes(replay, tool.Obj(review["replayFiles"]))
	plan := v.Read(source, "plan.json")
	newCapture := publicCapture(source)
	var captureBusiness tool.Object
	if newCapture {
		captureBusiness, _ = v.publicCaptureBusiness(source, replay, plan)
	}
	if automatic && !newCapture {
		v.Check(plan["recordingIsolation"] == "personal-skills-v1", "automatic qualification requires a recording-time isolated capture")
	}
	catalog, err := c.Catalog()
	if err != nil {
		return nil, err
	}
	definition := tool.Obj(catalog[tool.Str(plan["provider"])+"/"+tool.Str(plan["caseId"])])
	story := plan["storyId"]
	if story == nil {
		story = plan["story"]
	}
	v.Check(definition["story"] == story && (definition["requirement"] == "required" || definition["requirement"] == "optional"), "unknown or wrongly owned case")
	v.Check(plan["driverProfile"] == definition["driverProfile"], "capture driver profile differs from case")
	manifest := v.Read(source, "manifest.json")
	captureStatus := "passed"
	if newCapture {
		captureStatus = "captured"
	}
	v.Check(manifest["provenance"] == "capture" && manifest["status"] == captureStatus && manifest["captureStatus"] == "complete" && manifest["credentialScan"] == "pass", "real source capture, business pass and credential scan required")
	v.Check(manifest["planSHA256"] == v.Digest(source, "plan.json"), "source plan hash differs")
	if !newCapture {
		summary := v.Summary(source, "record", plan)
		v.Check(summary["recordingSHA256"] == v.Digest(source, "raw/raw.jsonl"), "capture summary hash differs")
	} else {
		report := v.Read(source, "report.json")
		v.Check(report["status"] == "captured" && report["captureStatus"] == "complete" && report["provenance"] == "official", "sealed capture report is incomplete")
	}
	replayReport := "replay-report.json"
	var verification tool.Object
	if tool.ReadJSON(filepath.Join(replay, "verification.json"), &verification) == nil && verification["pipeline"] == "public-agent-mock" {
		replayReport = "driver/replay-report.json"
	}
	for _, item := range [][2]string{{source, "report.json"}, {replay, replayReport}} {
		business := captureBusiness
		if item[0] != source || !newCapture {
			business = v.Read(item[0], item[1])
		}
		sdk := tool.Obj(business["officialSDK"])
		packageName := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[tool.Str(plan["provider"])]
		v.Check(business["status"] == "passed" && business["branchStatus"] == "passed" && business["captureStatus"] == "complete" && len(tool.Array(business["gaps"])) == 0 && business["provider"] == plan["provider"] && business["caseId"] == plan["caseId"] && business["storyId"] == story && sdk["package"] == packageName && tool.Str(sdk["version"]) != "", "official SDK business evidence is absent, untriggered or incomplete")
		if item[1] == "report.json" {
			v.Check(reflect.DeepEqual(sdk, review["officialSDK"]), "review SDK identity differs from capture")
		}
	}
	remote := tool.Object{}
	if newCapture && plan["driverProfile"] == "remote-close" {
		remote = tool.Obj(v.Read(replay, "driver/record-origin/run/result.json")["remoteService"])
		cleanup := tool.Obj(remote["cleanup"])
		v.Check(remote["owned"] == true && remote["cleanupComplete"] == true && cleanup["serverExitCode"] == float64(0) && cleanup["forcedCleanup"] == false, "owned remote service cleanup incomplete")
	}
	for _, phase := range Phases(plan) {
		rawroot := filepath.Join(source, phase[1], "raw")
		m := v.Read(rawroot, "manifest.json")
		transport := v.Read(rawroot, "transport.json")
		v.Check(reflect.DeepEqual(m["recordingFormat"], tool.Object{"name": "agent-mock-jsonl", "version": float64(1), "transport": "stdio-jsonl"}) && m["captureStatus"] == "complete", "unsupported or incomplete capture format")
		native := tool.Obj(m["native"])
		cwd := plan["project"]
		if plan["driverProfile"] == "existing-children" {
			cwd = plan["historyProject"]
		}
		if newCapture && len(remote) == 0 {
			cwd = v.recordWorkspace
			v.Check(native["sha256"] == plan["cliSHA256"] && reflect.DeepEqual(native["args"], plan["args"]), "observed native launch differs from isolated preparation")
			v.Check(transport["nativeExitCode"] == float64(0), "native capture exit was not successful")
		}
		if len(remote) > 0 {
			native = tool.Obj(remote["native"])
			cwd = v.recordWorkspace
			expectedArgs := []string{}
			args := tool.Strings(plan["args"])
			for i := 0; i < len(args); i++ {
				if args[i] == "--listen" {
					i++
					continue
				}
				if args[i] != "--stdio" {
					expectedArgs = append(expectedArgs, args[i])
				}
			}
			v.Check(native["sha256"] == plan["cliSHA256"] && reflect.DeepEqual(tool.Strings(native["args"]), expectedArgs) && native["transport"] == "websocket" && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tool.Str(native["privateArgumentsSHA256"])), "owned remote native launch differs")
			v.Check(transport["nativeLaunched"] == false && transport["remoteConnected"] == true && transport["remoteClosed"] == true, "remote connection did not settle")
		}
		v.Check(tool.Str(native["executable"]) != "" && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tool.Str(native["sha256"])) && tool.Str(cwd) != "" && native["cwd"] == cwd, "observed native execution identity required")
		v.Hashes(rawroot, tool.Obj(m["files"]))
		for _, name := range []string{"raw.jsonl", "stderr.raw", "transport.json"} {
			v.Check(tool.Obj(m["files"])[name] != nil, "raw manifest omits required artifact")
		}
		v.Check(transport["captureStatus"] == "complete" && transport["cleanupComplete"] == true && (len(remote) > 0 || (transport["nativeLaunched"] == true && transport["reaped"] == true)) && transport["clientEOF"] == true && transport["error"] == nil, "capture or cleanup incomplete")
		if v.Err == nil {
			b, e := os.ReadFile(filepath.Join(rawroot, "raw.jsonl"))
			v.Err = e
			rows := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
			v.Check(len(b) > 0 && transport["frames"] == float64(len(rows)), "capture frame count differs")
			for i, line := range rows {
				var row tool.Object
				if e := strictCorpusFrame(line, &row); e != nil {
					v.Err = e
					break
				}
				v.Check(len(row) == 6 && row["ordinal"] == float64(i+1) && row["epoch"] == float64(1) && row["channel"] == "protocol" && (row["direction"] == "c2s" || row["direction"] == "s2c") && row["elapsedMs"] != nil && len(tool.Obj(row["wire"])) > 0, "only original capture JSONL envelopes may be admitted")
			}
		}
	}
	v.Hashes(filepath.Join(source, "raw"), tool.Obj(manifest["files"]))
	if plan["driverProfile"] == "remote-close" && !newCapture {
		s := v.Read(source, "remote-service.json")
		v.Check(s["cleanupComplete"] == true && s["serverExitCode"] == float64(0) && s["forcedCleanup"] == false, "remote source cleanup incomplete")
	}
	v.Replay(source, replay, plan, "replay")
	required := v.Required(source, plan)
	names := tool.Strings(review["publishFiles"])
	selected := corpusSet(names)
	v.Check(len(selected) == len(names), "duplicate publication path")
	for name := range required {
		v.Check(selected[name] == true, "review omits reusable materials")
	}
	if automatic {
		v.Check(reflect.DeepEqual(selected, required), "automatic qualification selects only required regression materials")
	}
	for name := range selected {
		v.Check(corpusPublishable(name), "file is not publishable regression material: "+name)
		v.Digest(source, name)
	}
	v.Check(reflect.DeepEqual(corpusSet(Keys(tool.Obj(review["replayFiles"]))), v.ReplayEvidence(replay, plan)), "review must select only official replay evidence files")
	return plan, v.Err
}

func strictCorpusFrame(b []byte, row *tool.Object) error {
	// Reuse the strict evidence decoder, including duplicate-member rejection.
	if _, err := rawjson.Parse(b); err != nil {
		return err
	}
	return json.Unmarshal(b, row)
}

func (c *Corpus) Qualification(source, replay string, manual bool) (tool.Object, error) {
	if material, err := ReadMaterialDescriptor(source); err == nil && material != nil && material.FormatVersion == 3 {
		return c.qualifyPackage(source, replay, manual)
	}
	v := &Validation{}
	plan := v.Read(source, "plan.json")
	capture := v.Inventory(source, false)
	replayFiles := tool.Object{}
	for name := range v.ReplayEvidence(replay, plan) {
		replayFiles[name] = v.Digest(replay, name)
	}
	review := tool.Object{"version": float64(1), "origin": "real-agent", "qualification": tool.Object{"method": "official-sdk", "qualifiedAt": time.Now().UTC().Format(time.RFC3339Nano)}, "officialSDK": qualificationSDK(v, source, replay), "captureFiles": capture, "replayFiles": replayFiles, "publishFiles": Keys(v.Required(source, plan))}
	if v.Err != nil {
		return nil, v.Err
	}
	if manual {
		delete(review, "qualification")
		review["origin"] = "unreviewed"
		review["reviewer"] = ""
		review["reviewedAt"] = ""
		for _, field := range []string{"sourceReviewed", "contentReviewed", "credentialsReviewed"} {
			review[field] = false
		}
		return review, nil
	}
	_, err := c.ValidateAdmission(source, replay, review)
	return review, err
}

func corpusCopy(source, target string, names []string) error {
	for _, name := range names {
		p, err := Regular(source, name)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if err := errors.Join(err, f.Close()); err != nil {
			return err
		}
	}
	return nil
}

// VerifyAdoptedFiles validates the reduced immutable view. External mock may
// reuse its saved-history hashes without claiming official/publication eligibility.
func VerifyAdoptedFiles(root string) (tool.Object, error) {
	var adoptionVersion tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "adoption.json"), &adoptionVersion); err == nil && adoptionVersion["schemaVersion"] == float64(2) {
		material, err := ReadMaterialDescriptor(filepath.Join(root, "material"))
		if err != nil {
			return nil, err
		}
		if material == nil || material.FormatVersion != 3 {
			return nil, errors.New("schema 2 adoption requires a format 3 material descriptor")
		}
		if err := ValidateCore(material.Path, *material); err != nil {
			return nil, err
		}
		if err := ValidatePackage(root, *material); err != nil {
			return nil, err
		}
		return Read(filepath.Join(root, "review.json"))
	}
	v := &Validation{}
	adoption := v.Read(root, "adoption.json")
	v.Check(adoption["adoption"] == "admitted" && adoption["transformations"] != nil && len(tool.Array(adoption["transformations"])) == 0, "unsupported adoption record")
	files := v.Inventory(root, false)
	delete(files, "adoption.json")
	v.Check(reflect.DeepEqual(files, adoption["files"]), "adopted corpus inventory drift")
	review := v.Read(root, "review.json")
	original := tool.Obj(review["captureFiles"])
	v.Check(reflect.DeepEqual(original, adoption["originalFiles"]), "original provenance hash inventory drift")
	reduced := tool.Object{}
	for _, name := range tool.Strings(review["publishFiles"]) {
		reduced[name] = original[name]
	}
	v.Hashes(root, reduced)
	if v.Err != nil {
		return nil, v.Err
	}
	return review, nil
}

func (c *Corpus) VerifyAdopted(entry tool.Object) (string, error) {
	name := tool.Str(entry["materials"])
	if !Local(name) || !strings.HasPrefix(name, tool.Str(entry["provider"])+"/"+tool.Str(entry["storyId"])+"/"+tool.Str(entry["caseId"])+"/") {
		return "", errors.New("material ownership path differs")
	}
	base := filepath.Dir(c.Index)
	v := &Validation{}
	v.Check(v.Digest(base, name+"/adoption.json") == entry["adoptionSHA256"], "adoption hash drift")
	root := filepath.Join(base, name)
	if v.Err != nil {
		return "", v.Err
	}
	review, err := VerifyAdoptedFiles(root)
	if err != nil {
		return "", err
	}
	if review["schemaVersion"] == float64(2) {
		selectionBytes, err := json.Marshal(review)
		if err != nil {
			return "", err
		}
		var selection PackageSelection
		if err := json.Unmarshal(selectionBytes, &selection); err != nil {
			return "", err
		}
		plan, err := c.validatePackageAdmission(filepath.Join(root, "material"), filepath.Join(root, filepath.Dir(selection.Verification.Path)), review)
		if err != nil {
			return "", err
		}
		if plan["provider"] != entry["provider"] || plan["caseId"] != entry["caseId"] || plan["storyId"] != entry["storyId"] {
			return "", errors.New("adopted identity differs")
		}
		return root, nil
	}
	original := tool.Obj(review["captureFiles"])
	reduced := tool.Object{}
	for _, name := range tool.Strings(review["publishFiles"]) {
		reduced[name] = original[name]
	}
	view, err := os.MkdirTemp("", "agent-sdk-testify-qualification-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(view)
	if err := corpusCopy(root, view, tool.Strings(review["publishFiles"])); err != nil {
		return "", err
	}
	review["captureFiles"] = reduced
	plan, err := c.ValidateAdmission(view, filepath.Join(root, "official-replay"), review)
	if err != nil {
		return "", err
	}
	story := plan["storyId"]
	if story == nil {
		story = plan["story"]
	}
	if plan["provider"] != entry["provider"] || plan["caseId"] != entry["caseId"] || story != entry["storyId"] {
		return "", errors.New("adopted identity differs")
	}
	return root, nil
}

func (c *Corpus) archiveRoot() (string, error) {
	archive, err := filepath.Abs(c.Archive)
	if err != nil {
		return "", err
	}
	// Resolve existing ancestors, including symlinked parents, before containment.
	archive, err = Resolved(archive)
	if err != nil {
		return "", err
	}
	index, err := Resolved(c.Index)
	if err != nil {
		return "", err
	}
	if Within(archive, filepath.Dir(index)) {
		return "", errors.New("private archive must be outside regression corpus")
	}
	for dir := filepath.Dir(index); ; dir = filepath.Dir(dir) {
		if _, e := os.Lstat(filepath.Join(dir, ".git")); e == nil {
			if Within(archive, dir) && !Within(archive, filepath.Join(dir, ".artifacts")) {
				return "", errors.New("in-checkout private archives must be under ignored .artifacts")
			}
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return archive, nil
}

func Resolved(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", err
	}
	parent, err = Resolved(parent)
	return filepath.Join(parent, filepath.Base(abs)), err
}

func corpusAttempt(base string) (string, error) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, time.Now().UTC().Format("20060102T150405Z-"))
}

func (c *Corpus) Adopt(source, replay, reviewPath string) (target string, err error) {
	return c.AdoptWithIndexReplacement(source, replay, reviewPath, os.Rename)
}

func (c *Corpus) AdoptWithIndexReplacement(source, replay, reviewPath string, replace func(string, string) error) (target string, err error) {
	if review, err := Read(reviewPath); err == nil && review["schemaVersion"] == float64(2) {
		return c.adoptPackage(source, replay, reviewPath, replace)
	}
	archive, err := c.archiveRoot()
	if err != nil {
		return "", err
	}
	source, err = Resolved(source)
	if err != nil {
		return "", err
	}
	if Within(archive, source) {
		return "", errors.New("archive cannot be inside source capture")
	}
	original, err := Inventory(source, false)
	if err != nil {
		return "", err
	}
	saved, err := corpusAttempt(archive)
	if err != nil {
		return "", err
	}
	defer func() {
		status := "admitted"
		if err != nil {
			status = "rejected"
		}
		err = errors.Join(err, Write(filepath.Join(saved, "adoption-result.json"), tool.Object{"status": status, "materials": target}))
	}()
	if err := corpusCopy(source, filepath.Join(saved, "capture"), Keys(original)); err != nil {
		return "", err
	}
	if err := Write(filepath.Join(saved, "original-files.json"), original); err != nil {
		return "", err
	}
	review, err := Read(reviewPath)
	if err != nil {
		return "", err
	}
	if err := Write(filepath.Join(saved, "review.json"), review); err != nil {
		return "", err
	}
	v := &Validation{}
	v.Hashes(replay, tool.Obj(review["replayFiles"]))
	if v.Err != nil {
		return "", v.Err
	}
	if err := corpusCopy(replay, filepath.Join(saved, "official-replay"), Keys(tool.Obj(review["replayFiles"]))); err != nil {
		return "", err
	}
	plan, err := c.ValidateAdmission(filepath.Join(saved, "capture"), filepath.Join(saved, "official-replay"), review)
	if err != nil {
		return "", err
	}
	current, err := Inventory(source, false)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(current, original) {
		return "", errors.New("capture changed during adoption")
	}
	lockPath := strings.TrimSuffix(c.Index, filepath.Ext(c.Index)) + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()
	index, err := c.LoadIndex()
	if err != nil {
		return "", err
	}
	var entry tool.Object
	for _, item := range tool.Array(index["cases"]) {
		e := tool.Obj(item)
		if e["provider"] == plan["provider"] && e["caseId"] == plan["caseId"] {
			entry = e
			break
		}
	}
	if entry == nil {
		return "", errors.New("case absent from corpus index")
	}
	previous := ""
	var previousHashes tool.Object
	if entry["adoption"] == "admitted" {
		previous, err = c.VerifyAdopted(entry)
		if err != nil {
			return "", err
		}
		previousHashes, err = Inventory(previous, false)
		if err != nil {
			return "", err
		}
		if err := corpusCopy(previous, filepath.Join(saved, "previous-regression"), Keys(previousHashes)); err != nil {
			return "", err
		}
		if err := Write(filepath.Join(saved, "previous-entry.json"), entry); err != nil {
			return "", err
		}
	}
	name := tool.Str(entry["provider"]) + "/" + tool.Str(entry["storyId"]) + "/" + tool.Str(entry["caseId"]) + "/" + filepath.Base(saved)
	target = filepath.Join(filepath.Dir(c.Index), name)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(target)
		}
	}()
	if err := corpusCopy(filepath.Join(saved, "capture"), target, tool.Strings(review["publishFiles"])); err != nil {
		return target, err
	}
	if err := corpusCopy(filepath.Join(saved, "official-replay"), filepath.Join(target, "official-replay"), Keys(tool.Obj(review["replayFiles"]))); err != nil {
		return target, err
	}
	if err := Write(filepath.Join(target, "review.json"), review); err != nil {
		return target, err
	}
	files, err := Inventory(target, false)
	if err != nil {
		return target, err
	}
	record := tool.Object{"version": 1, "adoption": "admitted", "archiveId": filepath.Base(saved), "originalFiles": original, "transformations": []any{}, "files": files}
	if err := Write(filepath.Join(target, "adoption.json"), record); err != nil {
		return target, err
	}
	hash, err := Digest(filepath.Join(target, "adoption.json"))
	if err != nil {
		return target, err
	}
	entry["adoption"] = "admitted"
	entry["materials"] = name
	entry["adoptionSHA256"] = hash
	// Re-adopting identical frames cannot clear a known business evidence gap.
	if previous == "" || previousHashes["raw/raw.jsonl"] != files["raw/raw.jsonl"] || entry["verification"] == "passed" {
		entry["verification"] = "passed"
		delete(entry, "verificationReason")
	}
	temp, err := os.CreateTemp(filepath.Dir(c.Index), ".index-*")
	if err != nil {
		return target, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	b, err := json.MarshalIndent(index, "", "  ")
	if err == nil {
		_, err = temp.Write(append(b, '\n'))
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return target, err
	}
	if previous != "" {
		current, err = Inventory(previous, false)
		if err != nil {
			return target, err
		}
		if !reflect.DeepEqual(current, previousHashes) {
			return target, errors.New("previous sample changed during replacement")
		}
	}
	if err := replace(tempName, c.Index); err != nil {
		return target, err
	}
	committed = true
	if previous != "" {
		if err := os.RemoveAll(previous); err != nil {
			return target, fmt.Errorf("new index committed, old sample cleanup failed: %w", err)
		}
	}
	return target, nil
}

func qualificationSDK(v *Validation, source, replay string) any {
	if publicCapture(source) {
		return v.Read(replay, "driver/record-origin/run/driver/report.json")["officialSDK"]
	}
	return v.Read(source, "report.json")["officialSDK"]
}
