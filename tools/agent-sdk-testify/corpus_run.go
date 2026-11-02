package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

func corpusDefaultArchive() string {
	base := os.Getenv("AGENT_SDK_TESTIFY_DATA_DIR")
	if base == "" {
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
		base = filepath.Join(base, "agent-sdk-testify")
	}
	return filepath.Join(base, "archives")
}

func (c *corpus) flags(f *flag.FlagSet) {
	f.StringVar(&c.Index, "index", "", "fixed corpus index; record with this flag automatically replays and replaces qualified entries")
	f.StringVar(&c.Scenarios, "scenarios", "", "shared scenario catalog for corpus validation (default: index sibling ../scenarios)")
	f.StringVar(&c.Archive, "archive", corpusDefaultArchive(), "private archive outside the regression corpus")
}

func (c *corpus) normalize() error {
	if c.Index == "" {
		return errors.New("corpus index is required")
	}
	var err error
	c.Index, err = filepath.Abs(c.Index)
	if err != nil {
		return err
	}
	if c.Scenarios == "" {
		c.Scenarios = filepath.Join(filepath.Dir(filepath.Dir(c.Index)), "scenarios")
	}
	c.Scenarios, err = filepath.Abs(c.Scenarios)
	if err != nil {
		return err
	}
	c.Archive, err = c.archiveRoot()
	return err
}

func executeCorpus(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("use corpus qualify, review, adopt or gate")
	}
	c := corpus{}
	f := flag.NewFlagSet("corpus "+args[0], flag.ContinueOnError)
	c.flags(f)
	source := f.String("source", "", "original capture directory")
	replay := f.String("replay", "", "official SDK replay directory")
	reviewPath := f.String("review", "", "qualification proof or manual review JSON")
	output := f.String("output", "", "new gate output directory")
	driver := f.String("sdk-driver", "", "current Go SDK simulation driver executable")
	f.StringVar(&c.SourceRoot, "source-root", "", "optional checkout to bind gate source hashes; standalone binaries do not require it")
	jobs := f.Int("jobs", 2, "parallel simulation cases")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected corpus arguments")
	}
	if err := c.normalize(); err != nil {
		return err
	}
	switch args[0] {
	case "qualify", "review":
		if *source == "" || *replay == "" || *reviewPath == "" {
			return errors.New("source, replay and review are required")
		}
		review, err := c.qualification(*source, *replay, args[0] == "review")
		if err != nil {
			return err
		}
		return corpusWrite(*reviewPath, review)
	case "adopt":
		if *source == "" || *replay == "" || *reviewPath == "" {
			return errors.New("source, replay and review are required")
		}
		p, err := c.adopt(*source, *replay, *reviewPath)
		if err == nil {
			fmt.Println(p)
		}
		return err
	case "gate":
		if *driver == "" || *jobs < 1 {
			return errors.New("sdk-driver and positive jobs are required")
		}
		return c.gate(ctx, *output, *driver, *jobs)
	default:
		return errors.New("unknown corpus command")
	}
}

// Passing recordings are independently admitted even when another batch item
// fails. This never invokes a real Agent during replay or Go simulation.
func (c corpus) recordAndAdopt(ctx context.Context, o options) (object, error) {
	if _, err := c.loadIndex(); err != nil {
		return nil, err
	}
	if _, err := c.archiveRoot(); err != nil {
		return nil, err
	}
	var summary object
	var captureErr error
	if o.CasesFile != "" {
		summary, captureErr = runBatch(ctx, o)
	} else {
		summary, captureErr = run(ctx, o)
	}
	if summary == nil {
		return nil, captureErr
	}
	rows := []object{summary}
	if batch, ok := summary["results"].([]object); ok {
		rows = batch
	}
	results := make([]object, 0, len(rows))
	passed := 0
	for _, row := range rows {
		item := object{"provider": row["provider"], "case": row["case"], "status": "failed"}
		if err := c.admitRecording(ctx, row); err != nil {
			item["error"] = err.Error()
		} else {
			item["status"] = "admitted"
			passed++
		}
		results = append(results, item)
	}
	report := object{"mode": "record-and-adopt", "results": results, "passed": passed, "selected": len(rows), "output": summary["output"], "capture": summary}
	writeErr := corpusWrite(filepath.Join(str(summary["output"]), "admission-summary.json"), report)
	if passed != len(rows) {
		return report, errors.Join(captureErr, writeErr, fmt.Errorf("%d/%d recordings admitted", passed, len(rows)))
	}
	return report, errors.Join(captureErr, writeErr)
}

func (c corpus) admitRecording(ctx context.Context, row object) error {
	// run() stores a typed result; reports read back from disk store an object.
	status := ""
	if r, ok := row["result"].(result); ok {
		status = r.Status
	} else {
		status = str(obj(row["result"])["status"])
	}
	if row["error"] != nil || status != "passed" {
		return errors.New("recording did not pass")
	}
	source := str(row["output"])
	resolved, err := corpusResolved(source)
	if err != nil {
		return err
	}
	if corpusWithin(c.Archive, resolved) {
		return errors.New("archive cannot be inside source capture")
	}
	attempt, err := corpusAttempt(filepath.Join(c.Archive, "qualification"))
	if err != nil {
		return err
	}
	o := options{Mode: "replay", Materials: source, Output: filepath.Join(attempt, "replay"), Driver: str(row["driver"]), DriverArgs: stringsOf(row["driverArgs"])}
	o.Peer, err = os.Executable()
	if err != nil {
		return err
	}
	replayed, replayErr := run(ctx, o)
	if err = corpusWrite(filepath.Join(attempt, "replay-result.json"), object{"summary": replayed, "error": errorText(replayErr)}); err != nil {
		return err
	}
	if replayErr != nil {
		return replayErr
	}
	review, err := c.qualification(source, o.Output, false)
	if err != nil {
		return err
	}
	reviewPath := filepath.Join(attempt, "qualification.json")
	if err = corpusWrite(reviewPath, review); err != nil {
		return err
	}
	_, err = c.adopt(source, o.Output, reviewPath)
	return err
}

func errorText(err error) any {
	if err != nil {
		return err.Error()
	}
	return nil
}

func (c corpus) gate(ctx context.Context, output, driver string, jobs int) (err error) {
	if output == "" {
		base := filepath.Join(c.Archive, "gates")
		if c.SourceRoot != "" {
			base = filepath.Join(c.SourceRoot, ".artifacts", "simulation", "gates")
		}
		output, err = corpusAttempt(base)
	} else {
		output, err = createOutput(output, "gate")
	}
	if err != nil {
		return err
	}
	report := object{"version": 1, "cases": []object{}, "status": "failed"}
	rows := []object{}
	defer func() {
		counts := object{}
		for _, row := range rows {
			key := str(row["status"])
			n, _ := counts[key].(int)
			counts[key] = n + 1
		}
		report["cases"] = rows
		report["counts"] = counts
		if err != nil {
			report["error"] = err.Error()
		}
		err = errors.Join(err, corpusWrite(filepath.Join(output, "coverage.json"), report))
		b, _ := json.Marshal(object{"status": report["status"], "counts": counts, "report": filepath.Join(output, "coverage.json")})
		fmt.Println(string(b))
	}()
	index, err := c.loadIndex()
	if err != nil {
		return err
	}
	hash, err := corpusDigest(c.Index)
	if err != nil {
		return err
	}
	report["indexSHA256"] = hash
	self, err := os.Executable()
	if err != nil {
		return err
	}
	driver, err = absoluteExecutable(driver)
	if err != nil {
		return err
	}
	binaries := object{}
	for name, p := range map[string]string{"agent-sdk-testify": self, "sdk-driver": driver} {
		h, e := corpusDigest(p)
		if e != nil {
			return e
		}
		binaries[name] = h
	}
	report["binaries"] = binaries
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				report["sourceRevision"] = setting.Value
			}
			if setting.Key == "vcs.modified" {
				report["sourceModified"] = setting.Value
			}
		}
	}
	if c.SourceRoot != "" {
		provenance, e := corpusSource(c.SourceRoot)
		if e != nil {
			return e
		}
		for key, value := range provenance {
			report[key] = value
		}
	}
	selected := []object{}
	var integrityErr error
	for _, item := range array(index["cases"]) {
		e := obj(item)
		row := object{"provider": e["provider"], "storyId": e["storyId"], "caseId": e["caseId"], "requirement": e["requirement"], "adoption": e["adoption"], "verification": e["verification"], "verificationReason": e["verificationReason"], "status": e["verification"], "simulationStatus": "not-run"}
		if e["adoption"] == "admitted" {
			material, e2 := c.verifyAdopted(e)
			if e2 != nil {
				row["status"] = "failed"
				row["simulationStatus"] = "invalid-material"
				row["error"] = e2.Error()
				integrityErr = errors.Join(integrityErr, e2)
			} else {
				row["simulationStatus"] = "pending"
				selected = append(selected, object{"case": material, "provider": e["provider"], "caseId": e["caseId"]})
			}
		}
		rows = append(rows, row)
	}
	if len(selected) > 0 {
		selection := filepath.Join(output, "cases.json")
		if err = corpusWrite(selection, selected); err != nil {
			return err
		}
		o := options{Mode: "simulate", CasesFile: selection, Driver: driver, Peer: self, Jobs: jobs, Output: filepath.Join(output, "runs")}
		_, simulateErr := runBatch(ctx, o)
		report["simulateError"] = errorText(simulateErr)
		for _, row := range rows {
			if row["simulationStatus"] != "pending" {
				continue
			}
			var material string
			for _, item := range selected {
				if item["provider"] == row["provider"] && item["caseId"] == row["caseId"] {
					material = str(item["case"])
					break
				}
			}
			v := &corpusValidation{}
			plan := v.read(material, "plan.json")
			v.replay(material, filepath.Join(o.Output, str(row["provider"])+"-"+str(row["caseId"])), plan, "simulate")
			row["simulationStatus"] = "passed"
			if v.err != nil {
				row["simulationStatus"] = "failed"
				row["error"] = v.err.Error()
			}
			// Reproducing a recording cannot upgrade its business verification.
			if row["verification"] == "passed" {
				row["status"] = row["simulationStatus"]
			}
		}
	}
	if err = errors.Join(integrityErr, ctx.Err(), corpusGateResult(rows)); err != nil {
		return err
	}
	report["status"] = "passed"
	return nil
}

func corpusGateResult(rows []object) error {
	required := 0
	var failures []string
	for _, row := range rows {
		if row["requirement"] == "required" {
			required++
			if row["verification"] != "passed" || row["adoption"] != "admitted" || row["simulationStatus"] != "passed" || row["status"] != "passed" {
				failures = append(failures, str(row["provider"])+"/"+str(row["caseId"])+": "+str(row["status"]))
			}
		}
	}
	if required == 0 {
		return errors.New("empty required corpus")
	}
	if len(failures) > 0 {
		return fmt.Errorf("required recording regression did not pass: %s", strings.Join(failures, "; "))
	}
	return nil
}

func corpusSource(root string) (object, error) {
	cmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	revision, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	files := object{}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".artifacts" {
				return filepath.SkipDir
			}
			return nil
		}
		name, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		if !strings.HasSuffix(name, ".go") && name != "go.mod" && name != "go.sum" && name != "mise.toml" {
			return nil
		}
		regular, e := corpusRegular(root, filepath.ToSlash(name))
		if e != nil {
			return e
		}
		h, e := corpusDigest(regular)
		files[filepath.ToSlash(name)] = h
		return e
	})
	return object{"sourceRevision": strings.TrimSpace(string(revision)), "sourceFiles": files}, err
}
