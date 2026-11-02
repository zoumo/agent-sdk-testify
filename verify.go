package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	protocolcmd "github.com/zoumo/agent-sdk-testify/protocol/command"
)

func executeVerify(ctx context.Context, args []string) error {
	o := options{Mode: "verify"}
	c := corpus{}
	f := flag.NewFlagSet("verify", flag.ContinueOnError)
	f.StringVar(&o.Materials, "recording", "", "existing recording directory")
	f.StringVar(&o.Output, "output", "", "new separate verification directory")
	f.DurationVar(&o.Timeout, "timeout", 0, "whole-task timeout; zero disables it")
	f.StringVar(&c.Index, "index", "", "optional fixed index: qualify and atomically adopt after verification")
	f.StringVar(&c.Scenarios, "scenarios", "", "scenario catalog for admission (default: index sibling ../scenarios)")
	f.StringVar(&c.Archive, "archive", corpusDefaultArchive(), "private admission archive")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || o.Materials == "" {
		return errors.New("verify requires --recording")
	}
	if o.Timeout < 0 {
		return errors.New("timeout must be nonnegative")
	}
	if c.Index == "" && c.Scenarios != "" {
		return errors.New("scenarios requires index")
	}
	if c.Index != "" {
		if err := c.normalize(); err != nil {
			return err
		}
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	summary, err := verifyRecording(ctx, o)
	if summary != nil && c.Index != "" {
		root := str(summary["output"])
		admission := object{"status": "rejected"}
		if err == nil {
			var review object
			review, err = c.qualification(o.Materials, root, false)
			reviewPath := filepath.Join(root, "qualification.json")
			if err == nil {
				err = corpusWrite(reviewPath, review)
			}
			if err == nil {
				var target string
				target, err = c.adopt(o.Materials, root, reviewPath)
				admission["materials"] = target
				if err == nil {
					admission["status"] = "admitted"
				}
			}
		}
		if err != nil {
			admission["error"] = err.Error()
		}
		err = errors.Join(err, writeJSON(filepath.Join(root, "admission-summary.json"), admission))
	}
	if summary != nil {
		b, _ := json.Marshal(summary)
		fmt.Println(string(b))
	}
	return err
}

// A new attempt owns all new evidence. Imported plans, raw bytes and replay
// results remain immutable, including their historical mode and SDK identity.
func verifyRecording(ctx context.Context, o options) (summary object, err error) {
	o.Mode = "verify"
	o.Materials, err = corpusResolved(o.Materials)
	if err != nil {
		return nil, err
	}
	outputPath := o.Output
	if outputPath == "" {
		outputPath, err = outputBase("verify")
		if err != nil {
			return nil, err
		}
	}
	{
		resolved, e := corpusResolved(outputPath)
		if e != nil {
			return nil, e
		}
		if resolved == o.Materials || corpusWithin(resolved, o.Materials) || corpusWithin(o.Materials, resolved) {
			return nil, errors.New("verification output must be separate from recording")
		}
	}
	o.Output, err = createOutput(o.Output, "verify")
	if err != nil {
		return nil, err
	}
	o.Output, err = filepath.EvalSymlinks(o.Output)
	if err != nil {
		return nil, err
	}
	o.OutputPrepared = true
	evidence := object{"schemaVersion": 1, "status": "failed", "recording": o.Materials, "output": o.Output}
	defer func() {
		if summary == nil {
			summary = object{"mode": "verify", "output": o.Output, "result": result{Status: "failed"}}
		}
		if err != nil {
			summary["error"] = err.Error()
			evidence["error"] = err.Error()
		}
		err = errors.Join(err, writeJSON(filepath.Join(o.Output, "summary.json"), summary))
		// Bind the final run evidence, including failures, without a hash cycle.
		hashes := object{}
		for _, name := range []string{"summary.json", "result.json", "replay-report.json", "replay-peer.json", "remote-probe/replay-peer.json", "materialization.json", "peer-main-lifecycle.json", "peer-probe-lifecycle.json"} {
			if digest, e := fileDigest(filepath.Join(o.Output, name)); e == nil {
				hashes[name] = digest
			}
		}
		evidence["files"] = hashes
		err = errors.Join(err, writeJSON(filepath.Join(o.Output, "verification.json"), evidence))
	}()
	self, e := os.Executable()
	if e != nil {
		return nil, e
	}
	digest, e := fileDigest(self)
	if e != nil {
		return nil, e
	}
	evidence["tool"] = object{"version": protocolcmd.Version, "sha256": digest}
	before, e := corpusInventory(o.Materials, false)
	if e != nil {
		return nil, e
	}
	evidence["sourceFiles"] = before
	if before["adoption.json"] != nil {
		v := &corpusValidation{}
		adoption := v.read(o.Materials, "adoption.json")
		files := object{}
		for name, digest := range before {
			if name != "adoption.json" {
				files[name] = digest
			}
		}
		v.check(reflect.DeepEqual(files, adoption["files"]), "adopted corpus inventory drift")
		review := v.read(o.Materials, "review.json")
		v.check(reflect.DeepEqual(review["captureFiles"], adoption["originalFiles"]), "original provenance hash inventory drift")
		if v.err != nil {
			return nil, v.err
		}
	}
	for _, name := range sourceEvidence {
		if before[name] == nil {
			return nil, fmt.Errorf("recording omits %s", name)
		}
	}
	// Missing manifests cannot turn an unbound raw file into a recording.
	var originalPlan object
	if e = readJSON(filepath.Join(o.Materials, "plan.json"), &originalPlan); e != nil {
		return nil, e
	}
	vSource := &corpusValidation{}
	manifest := vSource.read(o.Materials, "manifest.json")
	vSource.check(manifest["planSHA256"] == before["plan.json"], "source plan hash differs")
	for _, phase := range corpusPhases(originalPlan, "verify") {
		rawRoot := filepath.Join(o.Materials, phase[1], "raw")
		m := vSource.read(rawRoot, "manifest.json")
		vSource.hashes(rawRoot, obj(m["files"]))
		for _, name := range []string{"raw.jsonl", "transport.json", "stderr.raw"} {
			vSource.check(obj(m["files"])[name] != nil, "raw manifest omits required artifact: "+name)
		}
	}
	if vSource.err != nil {
		return nil, vSource.err
	}
	plan, e := loadMaterials(o)
	if e != nil {
		return nil, e
	}
	o.Provider, o.Case = str(plan["provider"]), str(plan["caseId"])
	evidence["provider"], evidence["case"] = o.Provider, o.Case
	catalog, e := loadCatalogCase(caseCatalog, o)
	if e != nil {
		return nil, e
	}
	if plan["driverProfile"] != catalog["driverProfile"] {
		return nil, errors.New("recording profile differs from case contract")
	}
	contracts := object{}
	for _, name := range []string{str(catalog["storyId"]) + ".json", "bindings/" + o.Provider + ".json"} {
		data, e := catalogFiles.ReadFile("tests/simulation/scenarios/" + name)
		if e != nil {
			return nil, e
		}
		hash := sha256.Sum256(data)
		contracts[name] = hex.EncodeToString(hash[:])
	}
	evidence["contracts"] = contracts
	o.Driver, e = resolveOfficialDriver(o.Provider)
	if e != nil {
		return nil, e
	}
	digest, e = fileDigest(o.Driver)
	if e != nil {
		return nil, e
	}
	evidence["driver"] = object{"path": o.Driver, "sha256": digest}
	summary, err = run(ctx, o)
	var business object
	businessErr := readJSON(filepath.Join(o.Output, "replay-report.json"), &business)
	evidence["officialSDK"] = business["officialSDK"]
	if summary != nil {
		evidence["driverProcess"] = summary["driverProcess"]
	}
	sdk := obj(business["officialSDK"])
	packageName := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[o.Provider]
	if businessErr == nil && (business["status"] != "passed" || business["branchStatus"] != "passed" || business["captureStatus"] != "complete" || len(array(business["gaps"])) != 0 || business["provider"] != plan["provider"] || business["caseId"] != plan["caseId"] || business["storyId"] != catalog["storyId"] || sdk["package"] != packageName || str(sdk["version"]) == "") {
		businessErr = errors.New("official SDK business evidence is absent or incomplete")
	}
	v := &corpusValidation{}
	v.replay(o.Materials, o.Output, plan, "verify")
	after, sourceErr := corpusInventory(o.Materials, false)
	if sourceErr == nil && !reflect.DeepEqual(before, after) {
		sourceErr = errors.New("source changed during verification")
	}
	err = errors.Join(err, businessErr, v.err, sourceErr)
	if err != nil {
		return summary, err
	}
	evidence["status"] = "passed"
	return summary, nil
}
