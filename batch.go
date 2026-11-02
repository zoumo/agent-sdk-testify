package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

func runBatch(ctx context.Context, o options) (object, error) {
	var items []struct {
		Case     string  `json:"case"`
		Provider string  `json:"provider"`
		CaseID   string  `json:"caseId"`
		Driver   command `json:"driver"`
	}
	if err := readJSON(o.CasesFile, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("empty batch")
	}
	var err error
	o.Output, err = createOutput(o.Output, o.Mode)
	if err != nil {
		return nil, err
	}
	// A typo or duplicate must not race two processes in the same output dir.
	seen := map[string]bool{}
	for i := range items {
		x := &items[i]
		if x.Driver.Command == "" && o.Driver == "" {
			return nil, fmt.Errorf("driver required for batch item %d", i+1)
		}
		local := o
		local.Materials = x.Case
		local.Case = x.CaseID
		local.Provider = x.Provider
		plan, e := loadMaterials(local)
		if e != nil {
			return nil, e
		}
		x.Provider = str(plan["provider"])
		x.CaseID = str(plan["caseId"])
		key := x.Provider + "-" + x.CaseID
		if !filepath.IsLocal(key) || filepath.Base(key) != key || seen[key] {
			return nil, fmt.Errorf("duplicate or invalid batch identity %q", key)
		}
		seen[key] = true
	}
	results := make([]object, len(items))
	queue := make(chan int)
	var workers sync.WaitGroup
	for range min(o.Jobs, len(items)) {
		workers.Go(func() {
			for i := range queue {
				x := items[i]
				local := o
				local.Materials = x.Case
				local.Provider = x.Provider
				local.Case = x.CaseID
				local.CasesFile = ""
				local.Output = filepath.Join(o.Output, x.Provider+"-"+x.CaseID)
				if x.Driver.Command != "" {
					local.Driver = x.Driver.Command
					local.DriverArgs = x.Driver.Args
				}
				summary, e := run(ctx, local)
				if summary == nil {
					summary = object{"case": x.CaseID, "provider": x.Provider}
				}
				if e != nil {
					summary["error"] = e.Error()
				}
				results[i] = summary
			}
		})
	}
	for i := range items {
		queue <- i
	}
	close(queue)
	workers.Wait()
	passed := 0
	for _, r := range results {
		if v, ok := r["result"].(result); ok && r["error"] == nil && v.Status == "passed" {
			passed++
		}
	}
	summary := object{"selected": len(items), "passed": passed, "mode": o.Mode, "output": o.Output, "results": results}
	if err = writeJSON(filepath.Join(o.Output, "summary.json"), summary); err != nil {
		return summary, err
	}
	if passed != len(items) {
		return summary, fmt.Errorf("%d/%d cases passed", passed, len(items))
	}
	return summary, nil
}
