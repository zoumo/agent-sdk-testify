package main

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

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/rawjson"
)

func (c corpus) validateAdmission(source, replay string, review object) (object, error) {
	v := &corpusValidation{}
	v.check(review["version"] == float64(1) && review["origin"] == "real-agent", "review must attest real Agent origin")
	qualification, automatic := review["qualification"]
	if automatic {
		q := obj(qualification)
		_, err := time.Parse(time.RFC3339Nano, str(q["qualifiedAt"]))
		v.check(q["method"] == "official-sdk" && err == nil, "unsupported qualification method or time")
	} else {
		_, err := time.Parse(time.RFC3339Nano, str(review["reviewedAt"]))
		v.check(strings.TrimSpace(str(review["reviewer"])) != "" && err == nil, "reviewer and review time required")
		for _, field := range []string{"sourceReviewed", "contentReviewed", "credentialsReviewed"} {
			v.check(review[field] == true, "explicit review required: "+field)
		}
	}
	v.check(reflect.DeepEqual(v.inventory(source, false), review["captureFiles"]), "capture inventory drifted or is incomplete")
	v.hashes(replay, obj(review["replayFiles"]))
	plan := v.read(source, "plan.json")
	if automatic {
		v.check(plan["recordingIsolation"] == "personal-skills-v1", "automatic qualification requires a recording-time isolated capture")
	}
	catalog, err := c.catalog()
	if err != nil {
		return nil, err
	}
	definition := obj(catalog[str(plan["provider"])+"/"+str(plan["caseId"])])
	story := plan["storyId"]
	if story == nil {
		story = plan["story"]
	}
	v.check(definition["story"] == story && (definition["requirement"] == "required" || definition["requirement"] == "optional"), "unknown or wrongly owned case")
	v.check(plan["driverProfile"] == definition["driverProfile"], "capture driver profile differs from case")
	manifest := v.read(source, "manifest.json")
	v.check(manifest["provenance"] == "capture" && manifest["status"] == "passed" && manifest["captureStatus"] == "complete" && manifest["credentialScan"] == "pass", "real source capture, business pass and credential scan required")
	v.check(manifest["planSHA256"] == v.digest(source, "plan.json"), "source plan hash differs")
	summary := v.summary(source, "record", plan)
	v.check(summary["recordingSHA256"] == v.digest(source, "raw/raw.jsonl"), "capture summary hash differs")
	for _, item := range [][2]string{{source, "report.json"}, {replay, "replay-report.json"}} {
		business := v.read(item[0], item[1])
		sdk := obj(business["officialSDK"])
		packageName := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[str(plan["provider"])]
		v.check(business["status"] == "passed" && business["branchStatus"] == "passed" && business["captureStatus"] == "complete" && len(array(business["gaps"])) == 0 && business["provider"] == plan["provider"] && business["caseId"] == plan["caseId"] && business["storyId"] == story && sdk["package"] == packageName && str(sdk["version"]) != "", "official SDK business evidence is absent, untriggered or incomplete")
		if item[1] == "report.json" {
			v.check(reflect.DeepEqual(sdk, review["officialSDK"]), "review SDK identity differs from capture")
		}
	}
	for _, phase := range corpusPhases(plan, "replay") {
		rawroot := filepath.Join(source, phase[1], "raw")
		m := v.read(rawroot, "manifest.json")
		transport := v.read(rawroot, "transport.json")
		v.check(reflect.DeepEqual(m["recordingFormat"], object{"name": "agent-mock-jsonl", "version": float64(1), "transport": "stdio-jsonl"}) && m["captureStatus"] == "complete", "unsupported or incomplete capture format")
		native := obj(m["native"])
		cwd := plan["project"]
		if plan["driverProfile"] == "existing-children" {
			cwd = plan["historyProject"]
		}
		v.check(str(native["executable"]) != "" && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(str(native["sha256"])) && str(cwd) != "" && native["cwd"] == cwd, "observed native execution identity required")
		v.hashes(rawroot, obj(m["files"]))
		for _, name := range []string{"raw.jsonl", "stderr.raw", "transport.json"} {
			v.check(obj(m["files"])[name] != nil, "raw manifest omits required artifact")
		}
		v.check(transport["captureStatus"] == "complete" && transport["cleanupComplete"] == true && transport["nativeLaunched"] == true && transport["reaped"] == true && transport["clientEOF"] == true && transport["error"] == nil, "capture or cleanup incomplete")
		if v.err == nil {
			b, e := os.ReadFile(filepath.Join(rawroot, "raw.jsonl"))
			v.err = e
			rows := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
			v.check(len(b) > 0 && transport["frames"] == float64(len(rows)), "capture frame count differs")
			for i, line := range rows {
				var row object
				if _, e := strictCorpusFrame(line, &row); e != nil {
					v.err = e
					break
				}
				v.check(len(row) == 6 && row["ordinal"] == float64(i+1) && row["epoch"] == float64(1) && row["channel"] == "protocol" && (row["direction"] == "c2s" || row["direction"] == "s2c") && row["elapsedMs"] != nil && len(obj(row["wire"])) > 0, "only original capture JSONL envelopes may be admitted")
			}
		}
	}
	v.hashes(filepath.Join(source, "raw"), obj(manifest["files"]))
	if plan["driverProfile"] == "remote-close" {
		s := v.read(source, "remote-service.json")
		v.check(s["cleanupComplete"] == true && s["serverExitCode"] == float64(0) && s["forcedCleanup"] == false, "remote source cleanup incomplete")
	}
	v.replay(source, replay, plan, "replay")
	required := v.required(source, plan)
	names := stringsOf(review["publishFiles"])
	selected := corpusSet(names)
	v.check(len(selected) == len(names), "duplicate publication path")
	for name := range required {
		v.check(selected[name] == true, "review omits reusable materials")
	}
	if automatic {
		v.check(reflect.DeepEqual(selected, required), "automatic qualification selects only required regression materials")
	}
	for name := range selected {
		v.check(corpusPublishable(name), "file is not publishable regression material: "+name)
		v.digest(source, name)
	}
	v.check(reflect.DeepEqual(corpusSet(corpusKeys(obj(review["replayFiles"]))), v.replayEvidence(replay, plan)), "review must select only official replay evidence files")
	return plan, v.err
}

func strictCorpusFrame(b []byte, row *object) (int, error) {
	// Reuse the strict evidence decoder, including duplicate-member rejection.
	if _, err := rawjson.Parse(b); err != nil {
		return 0, err
	}
	return len(b), json.Unmarshal(b, row)
}

func (c corpus) qualification(source, replay string, manual bool) (object, error) {
	v := &corpusValidation{}
	plan := v.read(source, "plan.json")
	capture := v.inventory(source, false)
	replayFiles := object{}
	for name := range v.replayEvidence(replay, plan) {
		replayFiles[name] = v.digest(replay, name)
	}
	review := object{"version": float64(1), "origin": "real-agent", "qualification": object{"method": "official-sdk", "qualifiedAt": time.Now().UTC().Format(time.RFC3339Nano)}, "officialSDK": v.read(source, "report.json")["officialSDK"], "captureFiles": capture, "replayFiles": replayFiles, "publishFiles": corpusKeys(v.required(source, plan))}
	if v.err != nil {
		return nil, v.err
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
	_, err := c.validateAdmission(source, replay, review)
	return review, err
}

func corpusCopy(source, target string, names []string) error {
	for _, name := range names {
		p, err := corpusRegular(source, name)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, name)
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
	}
	return nil
}

func (c corpus) verifyAdopted(entry object) (string, error) {
	name := str(entry["materials"])
	if !corpusLocal(name) || !strings.HasPrefix(name, str(entry["provider"])+"/"+str(entry["storyId"])+"/"+str(entry["caseId"])+"/") {
		return "", errors.New("material ownership path differs")
	}
	base := filepath.Dir(c.Index)
	v := &corpusValidation{}
	v.check(v.digest(base, name+"/adoption.json") == entry["adoptionSHA256"], "adoption hash drift")
	root := filepath.Join(base, name)
	adoption := v.read(root, "adoption.json")
	v.check(adoption["adoption"] == "admitted" && adoption["transformations"] != nil && len(array(adoption["transformations"])) == 0, "unsupported adoption record")
	files := v.inventory(root, false)
	delete(files, "adoption.json")
	v.check(reflect.DeepEqual(files, adoption["files"]), "adopted corpus inventory drift")
	review := v.read(root, "review.json")
	original := obj(review["captureFiles"])
	v.check(reflect.DeepEqual(original, adoption["originalFiles"]), "original provenance hash inventory drift")
	reduced := object{}
	for _, name := range stringsOf(review["publishFiles"]) {
		reduced[name] = original[name]
	}
	v.hashes(root, reduced)
	if v.err != nil {
		return "", v.err
	}
	view, err := os.MkdirTemp("", "agent-sdk-testify-qualification-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(view)
	if err = corpusCopy(root, view, stringsOf(review["publishFiles"])); err != nil {
		return "", err
	}
	review["captureFiles"] = reduced
	plan, err := c.validateAdmission(view, filepath.Join(root, "official-replay"), review)
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

func (c corpus) archiveRoot() (string, error) {
	archive, err := filepath.Abs(c.Archive)
	if err != nil {
		return "", err
	}
	// Resolve existing ancestors, including symlinked parents, before containment.
	archive, err = corpusResolved(archive)
	if err != nil {
		return "", err
	}
	index, err := corpusResolved(c.Index)
	if err != nil {
		return "", err
	}
	if corpusWithin(archive, filepath.Dir(index)) {
		return "", errors.New("private archive must be outside regression corpus")
	}
	for dir := filepath.Dir(index); ; dir = filepath.Dir(dir) {
		if _, e := os.Lstat(filepath.Join(dir, ".git")); e == nil {
			if corpusWithin(archive, dir) && !corpusWithin(archive, filepath.Join(dir, ".artifacts")) {
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

func corpusResolved(p string) (string, error) {
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
	parent, err = corpusResolved(parent)
	return filepath.Join(parent, filepath.Base(abs)), err
}

func corpusAttempt(base string) (string, error) {
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, time.Now().UTC().Format("20060102T150405Z-"))
}

func (c corpus) adopt(source, replay, reviewPath string) (target string, err error) {
	return c.adoptWithIndexReplacement(source, replay, reviewPath, os.Rename)
}

func (c corpus) adoptWithIndexReplacement(source, replay, reviewPath string, replace func(string, string) error) (target string, err error) {
	archive, err := c.archiveRoot()
	if err != nil {
		return "", err
	}
	source, err = corpusResolved(source)
	if err != nil {
		return "", err
	}
	if corpusWithin(archive, source) {
		return "", errors.New("archive cannot be inside source capture")
	}
	original, err := corpusInventory(source, false)
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
		err = errors.Join(err, corpusWrite(filepath.Join(saved, "adoption-result.json"), object{"status": status, "materials": target}))
	}()
	if err = corpusCopy(source, filepath.Join(saved, "capture"), corpusKeys(original)); err != nil {
		return "", err
	}
	if err = corpusWrite(filepath.Join(saved, "original-files.json"), original); err != nil {
		return "", err
	}
	review, err := corpusRead(reviewPath)
	if err != nil {
		return "", err
	}
	if err = corpusWrite(filepath.Join(saved, "review.json"), review); err != nil {
		return "", err
	}
	v := &corpusValidation{}
	v.hashes(replay, obj(review["replayFiles"]))
	if v.err != nil {
		return "", v.err
	}
	if err = corpusCopy(replay, filepath.Join(saved, "official-replay"), corpusKeys(obj(review["replayFiles"]))); err != nil {
		return "", err
	}
	plan, err := c.validateAdmission(filepath.Join(saved, "capture"), filepath.Join(saved, "official-replay"), review)
	if err != nil {
		return "", err
	}
	current, err := corpusInventory(source, false)
	if err != nil {
		return "", err
	}
	if !reflect.DeepEqual(current, original) {
		return "", errors.New("capture changed during adoption")
	}
	lockPath := strings.TrimSuffix(c.Index, filepath.Ext(c.Index)) + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()
	index, err := c.loadIndex()
	if err != nil {
		return "", err
	}
	var entry object
	for _, item := range array(index["cases"]) {
		e := obj(item)
		if e["provider"] == plan["provider"] && e["caseId"] == plan["caseId"] {
			entry = e
			break
		}
	}
	if entry == nil {
		return "", errors.New("case absent from corpus index")
	}
	previous := ""
	var previousHashes object
	if entry["adoption"] == "admitted" {
		previous, err = c.verifyAdopted(entry)
		if err != nil {
			return "", err
		}
		previousHashes, err = corpusInventory(previous, false)
		if err != nil {
			return "", err
		}
		if err = corpusCopy(previous, filepath.Join(saved, "previous-regression"), corpusKeys(previousHashes)); err != nil {
			return "", err
		}
		if err = corpusWrite(filepath.Join(saved, "previous-entry.json"), entry); err != nil {
			return "", err
		}
	}
	name := str(entry["provider"]) + "/" + str(entry["storyId"]) + "/" + str(entry["caseId"]) + "/" + filepath.Base(saved)
	target = filepath.Join(filepath.Dir(c.Index), name)
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", err
	}
	if err = os.Mkdir(target, 0700); err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(target)
		}
	}()
	if err = corpusCopy(filepath.Join(saved, "capture"), target, stringsOf(review["publishFiles"])); err != nil {
		return target, err
	}
	if err = corpusCopy(filepath.Join(saved, "official-replay"), filepath.Join(target, "official-replay"), corpusKeys(obj(review["replayFiles"]))); err != nil {
		return target, err
	}
	if err = corpusWrite(filepath.Join(target, "review.json"), review); err != nil {
		return target, err
	}
	files, err := corpusInventory(target, false)
	if err != nil {
		return target, err
	}
	record := object{"version": 1, "adoption": "admitted", "archiveId": filepath.Base(saved), "originalFiles": original, "transformations": []any{}, "files": files}
	if err = corpusWrite(filepath.Join(target, "adoption.json"), record); err != nil {
		return target, err
	}
	hash, err := corpusDigest(filepath.Join(target, "adoption.json"))
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
		current, err = corpusInventory(previous, false)
		if err != nil {
			return target, err
		}
		if !reflect.DeepEqual(current, previousHashes) {
			return target, errors.New("previous sample changed during replacement")
		}
	}
	if err = replace(tempName, c.Index); err != nil {
		return target, err
	}
	committed = true
	if previous != "" {
		if err = os.RemoveAll(previous); err != nil {
			return target, fmt.Errorf("new index committed, old sample cleanup failed: %w", err)
		}
	}
	return target, nil
}
