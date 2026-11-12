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

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func packageSelection(root, verificationID string, material Material) (PackageSelection, error) {
	selection := PackageSelection{SchemaVersion: 2, MaterialID: material.ID}
	if err := CheckMaterialInventory(filepath.Join(root, "material"), material); err != nil {
		return selection, err
	}
	if material.FormatVersion == 3 {
		if err := validatePackageRecord(root, material); err != nil {
			return selection, err
		}
	}
	if err := ValidateOfficialVerification(root, verificationID, material); err != nil {
		return selection, err
	}
	bind := func(path string) (PackageSelectionReference, error) {
		hash, err := tool.FileDigest(filepath.Join(root, filepath.FromSlash(path)))
		return PackageSelectionReference{Path: path, SHA256: hash}, err
	}
	var err error
	selection.Material, err = bind("material/material.json")
	if err != nil {
		return selection, err
	}
	if material.FormatVersion == 3 {
		selection.Record, err = bind("record/" + material.OfficialOrigin.RunID + "/result.json")
		if err != nil {
			return selection, err
		}
	}
	selection.Verification, err = bind("verify/" + verificationID + "/verification.json")
	if err != nil {
		return selection, err
	}
	var record, verification tool.Object
	if selection.Record.Path != "" {
		if err := tool.ReadJSON(filepath.Join(root, selection.Record.Path), &record); err != nil {
			return selection, err
		}
	}
	if err := tool.ReadJSON(filepath.Join(root, selection.Verification.Path), &verification); err != nil {
		return selection, err
	}
	ids := tool.Object{}
	if id := tool.Str(record["executionId"]); id != "" {
		ids[id] = true
	}
	for _, ref := range tool.Array(verification["executions"]) {
		ids[tool.Str(tool.Obj(ref)["executionId"])] = true
	}
	for _, id := range Keys(ids) {
		if !packageID(id) {
			return selection, errors.New("invalid selected execution identity")
		}
		ref, err := bind("executions/" + id + "/result.json")
		if err != nil {
			return selection, err
		}
		selection.Executions = append(selection.Executions, ref)
	}
	return selection, nil
}

func (c *Corpus) qualifyPackage(source, replay string, manual bool) (tool.Object, error) {
	if manual {
		return nil, errors.New("format 3 qualification requires settled official evidence")
	}
	material, err := ReadMaterialDescriptor(source)
	if err != nil {
		return nil, err
	}
	if material == nil {
		return nil, errors.New("package qualification requires a material descriptor")
	}
	root := filepath.Dir(source)
	if filepath.Dir(filepath.Dir(replay)) != root || filepath.Base(filepath.Dir(replay)) != "verify" {
		return nil, errors.New("verification is outside selected package")
	}
	selection, err := packageSelection(root, filepath.Base(replay), *material)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(selection)
	if err != nil {
		return nil, err
	}
	var review tool.Object
	if err := json.Unmarshal(data, &review); err != nil {
		return nil, err
	}
	review["origin"] = "real-agent"
	review["qualification"] = tool.Object{"method": "official-sdk", "qualifiedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	_, err = c.validatePackageAdmission(source, replay, review)
	return review, err
}

func (c *Corpus) validatePackageAdmission(source, replay string, review tool.Object) (tool.Object, error) {
	material, err := ReadMaterialDescriptor(source)
	if err != nil {
		return nil, err
	}
	if material == nil {
		return nil, errors.New("package qualification requires a material descriptor")
	}
	root := filepath.Dir(source)
	if filepath.Dir(filepath.Dir(replay)) != root || filepath.Base(filepath.Dir(replay)) != "verify" {
		return nil, errors.New("verification is outside selected package")
	}
	expected, err := packageSelection(root, filepath.Base(replay), *material)
	if err != nil {
		return nil, err
	}
	reviewBytes, err := json.Marshal(review)
	if err != nil {
		return nil, err
	}
	var selection PackageSelection
	if err := json.Unmarshal(reviewBytes, &selection); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(selection, expected) {
		return nil, errors.New("review selection drifted from exact official closure")
	}
	if _, ok := review["files"]; ok {
		return nil, errors.New("review duplicates domain inventory")
	}
	qualification := tool.Obj(review["qualification"])
	_, err = time.Parse(time.RFC3339Nano, tool.Str(qualification["qualifiedAt"]))
	if review["origin"] != "real-agent" || qualification["method"] != "official-sdk" || err != nil {
		return nil, errors.New("review must attest official qualification")
	}
	if material.FormatVersion != 3 {
		// Reuse every legacy capture/business/native policy check. This view is
		// transient: the persisted review only selects the sealed package domains.
		v := &Validation{}
		plan := v.Read(source, "plan.json")
		legacyReview := tool.Object{
			"version": float64(1), "origin": review["origin"], "qualification": qualification,
			"officialSDK": qualificationSDK(v, source, replay), "captureFiles": v.Inventory(source, false),
			"replayFiles": v.Inventory(replay, false), "publishFiles": Keys(v.Required(source, plan)),
		}
		if v.Err != nil {
			return nil, v.Err
		}
		return c.ValidateAdmission(source, replay, legacyReview)
	}
	var plan, manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(source, "plan.json"), &plan); err != nil {
		return nil, err
	}
	if err := tool.ReadJSON(filepath.Join(source, "manifest.json"), &manifest); err != nil {
		return nil, err
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
	if definition["story"] != story || (definition["requirement"] != "required" && definition["requirement"] != "optional") || plan["driverProfile"] != definition["driverProfile"] || manifest["provenance"] != "capture" || manifest["status"] != "captured" || manifest["captureStatus"] != "complete" || manifest["credentialScan"] != "pass" {
		return nil, errors.New("capture is not an eligible complete official case")
	}
	var record, preparation tool.Object
	if err := tool.ReadJSON(filepath.Join(root, expected.Record.Path), &record); err != nil {
		return nil, err
	}
	if err := tool.ReadJSON(filepath.Join(root, "executions", tool.Str(record["executionId"]), "isolation.json"), &preparation); err != nil {
		return nil, err
	}
	v := &Validation{}
	for _, phase := range Phases(plan) {
		raw := filepath.Join(source, phase[1], "raw")
		m := v.Read(raw, "manifest.json")
		transport := v.Read(raw, "transport.json")
		v.Check(reflect.DeepEqual(m["recordingFormat"], tool.Object{"name": "agent-mock-jsonl", "version": float64(1), "transport": "stdio-jsonl"}) && m["captureStatus"] == "complete", "unsupported or incomplete capture format")
		v.Hashes(raw, tool.Obj(m["files"]))
		for _, name := range []string{"raw.jsonl", "transport.json", "stderr.raw"} {
			v.Check(tool.Obj(m["files"])[name] != nil, "raw manifest omits required artifact")
		}
		native := tool.Obj(m["native"])
		remote := tool.Obj(record["remoteService"])
		if len(remote) > 0 {
			cleanup := tool.Obj(remote["cleanup"])
			v.Check(remote["owned"] == true && remote["cleanupComplete"] == true && cleanup["serverExitCode"] == float64(0) && cleanup["forcedCleanup"] == false, "owned remote cleanup incomplete")
			native = tool.Obj(remote["native"])
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
			v.Check(reflect.DeepEqual(tool.Strings(native["args"]), expectedArgs) && native["transport"] == "websocket" && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tool.Str(native["privateArgumentsSHA256"])), "owned remote native launch differs")
			v.Check(transport["nativeLaunched"] == false && transport["remoteConnected"] == true && transport["remoteClosed"] == true, "remote connection did not settle")
		} else {
			v.Check(reflect.DeepEqual(native["args"], plan["args"]) && transport["nativeLaunched"] == true && transport["reaped"] == true && transport["nativeExitCode"] == float64(0), "native invocation or exit differs")
		}
		v.Check(native["sha256"] == plan["cliSHA256"] && native["cwd"] == preparation["cwd"] && tool.Str(native["executable"]) != "", "observed native identity differs")
		v.Check(transport["captureStatus"] == "complete" && transport["cleanupComplete"] == true && transport["clientEOF"] == true && transport["error"] == nil, "capture or cleanup incomplete")
		data, err := os.ReadFile(filepath.Join(raw, "raw.jsonl"))
		if err != nil {
			return nil, err
		}
		rows := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
		v.Check(len(data) > 0 && transport["frames"] == float64(len(rows)), "capture frame count differs")
		for i, line := range rows {
			var row tool.Object
			if err := strictCorpusFrame(line, &row); err != nil {
				return nil, err
			}
			v.Check(len(row) == 6 && row["ordinal"] == float64(i+1) && row["epoch"] == float64(1) && row["channel"] == "protocol" && (row["direction"] == "c2s" || row["direction"] == "s2c") && row["elapsedMs"] != nil && len(tool.Obj(row["wire"])) > 0, "only original capture JSONL envelopes may be admitted")
		}
	}
	return plan, v.Err
}

// Selected domains keep their package-relative layout and original bytes. The
// review binds envelopes, which each retain their own authoritative inventory.
func copySelectedPackage(source, target string, selection PackageSelection) error {
	if err := tool.CopyTree(filepath.Join(source, "material"), filepath.Join(target, "material")); err != nil {
		return err
	}
	refs := append([]PackageSelectionReference{selection.Record, selection.Verification}, selection.Executions...)
	for _, ref := range refs {
		if ref.Path == "" {
			continue
		}
		dir := filepath.Dir(filepath.FromSlash(ref.Path))
		if err := tool.CopyTree(filepath.Join(source, dir), filepath.Join(target, dir)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Corpus) adoptPackage(source, replay, reviewPath string, replace func(string, string) error) (string, error) {
	review, err := Read(reviewPath)
	if err != nil {
		return "", err
	}
	plan, err := c.validatePackageAdmission(source, replay, review)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(reviewPath)
	if err != nil {
		return "", err
	}
	var selection PackageSelection
	if err := json.Unmarshal(b, &selection); err != nil {
		return "", err
	}
	reviewHash := tool.DigestBytes(b)
	archive, err := c.archiveRoot()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(source)
	if Within(archive, root) {
		return "", errors.New("archive cannot be inside selected package")
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
	if entry["adoption"] == "admitted" {
		if _, err := c.VerifyAdopted(entry); err != nil {
			return "", err
		}
	}
	attempt := reviewHash[:24]
	name := filepath.ToSlash(filepath.Join(tool.Str(entry["provider"]), tool.Str(entry["storyId"]), tool.Str(entry["caseId"]), attempt))
	target := filepath.Join(filepath.Dir(c.Index), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".attempt-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := copySelectedPackage(root, stage, selection); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "review.json"), b, 0o600); err != nil {
		return "", err
	}
	adoption := tool.Object{"schemaVersion": 2, "materialId": selection.MaterialID, "adoption": "admitted", "reviewSHA256": reviewHash, "provider": entry["provider"], "storyId": entry["storyId"], "caseId": entry["caseId"], "attemptId": attempt}
	if err := Write(filepath.Join(stage, "adoption.json"), adoption); err != nil {
		return "", err
	}
	material, err := ReadMaterialDescriptor(filepath.Join(stage, "material"))
	if err != nil {
		return "", err
	}
	if material == nil {
		return "", errors.New("selected package omits its material descriptor")
	}
	if err := ValidatePackage(stage, *material); err != nil {
		return "", err
	}
	if _, err := c.validatePackageAdmission(filepath.Join(stage, "material"), filepath.Join(stage, filepath.Dir(selection.Verification.Path)), review); err != nil {
		return "", err
	}
	// Archive the selected immutable facts before publishing the attempt.
	saved := filepath.Join(archive, attempt)
	if _, err := os.Stat(saved); os.IsNotExist(err) {
		if err := tool.CopyTree(stage, saved); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		before, e := Inventory(saved, false)
		if e != nil {
			return "", e
		}
		after, e := Inventory(stage, false)
		if e != nil {
			return "", e
		}
		if !reflect.DeepEqual(before, after) {
			return "", errors.New("archive identity collision")
		}
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if err := os.Rename(stage, target); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		before, e := Inventory(target, false)
		if e != nil {
			return "", e
		}
		after, e := Inventory(stage, false)
		if e != nil {
			return "", e
		}
		if !reflect.DeepEqual(before, after) {
			return "", errors.New("attempt identity collision")
		}
	}
	hash, err := tool.FileDigest(filepath.Join(target, "adoption.json"))
	if err != nil {
		return target, err
	}
	entry["adoption"], entry["materials"], entry["adoptionSHA256"], entry["verification"] = "admitted", name, hash, "passed"
	delete(entry, "verificationReason")
	temp, err := os.CreateTemp(filepath.Dir(c.Index), ".index-*")
	if err != nil {
		return target, err
	}
	defer os.Remove(temp.Name())
	encoded, err := json.MarshalIndent(index, "", "  ")
	if err == nil {
		_, err = temp.Write(append(encoded, '\n'))
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return target, err
	}
	if err := replace(temp.Name(), c.Index); err != nil {
		return target, fmt.Errorf("attempt published; index not committed: %w", err)
	}
	return target, nil
}
