package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type object = map[string]any

func obj(v any) object {
	m, _ := v.(map[string]any)
	if m == nil {
		return object{}
	}
	return m
}
func str(v any) string { s, _ := v.(string); return s }
func stringsOf(v any) []string {
	var out []string
	for _, x := range array(v) {
		out = append(out, str(x))
	}
	return out
}
func array(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if a, ok := v.([]string); ok {
		out := make([]any, len(a))
		for i, s := range a {
			out[i] = s
		}
		return out
	}
	return nil
}

// Existing plans supply materials and native identities, not runtime locks.
func loadMaterials(o options) (object, error) {
	if err := verifyMaterials(o.Materials, o.Mode != "record"); err != nil {
		return nil, err
	}
	var plan object
	if e := readJSON(filepath.Join(o.Materials, "plan.json"), &plan); e == nil {
		if o.Case != "" && o.Case != str(plan["caseId"]) {
			return nil, errors.New("case differs from selected recording")
		}
		if o.Provider != "" && o.Provider != str(plan["provider"]) {
			return nil, errors.New("provider differs from selected recording")
		}
		return plan, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if e := readJSON(filepath.Join(o.Materials, "case.json"), &plan); e == nil {
		return plan, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	paths, e := filepath.Glob(filepath.Join(o.Materials, "SDK-US-*.json"))
	if e != nil {
		return nil, e
	}
	sort.Strings(paths)
	var core, story object
	for _, p := range paths {
		var s object
		if e = readJSON(p, &s); e != nil {
			return nil, e
		}
		for _, c := range array(s["cases"]) {
			if str(obj(c)["id"]) == o.Case {
				core = obj(c)
				story = s
			}
		}
	}
	if core == nil {
		return nil, fmt.Errorf("case %q is absent from shared materials", o.Case)
	}
	var bindings object
	if e = readJSON(filepath.Join(o.Materials, "bindings", o.Provider+".json"), &bindings); e != nil {
		return nil, e
	}
	b, ok := obj(bindings["cases"])[o.Case]
	if !ok {
		return nil, errors.New("provider has no material binding for case")
	}
	binding := obj(b)
	merge := func(a, b any) object {
		out := object{}
		for k, v := range obj(a) {
			out[k] = v
		}
		for k, v := range obj(b) {
			out[k] = v
		}
		return out
	}
	inputs := obj(core["inputs"])
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	prompts := []any{}
	for _, k := range names {
		prompts = append(prompts, inputs[k])
	}
	if v, ok := binding["inputs"]; ok {
		prompts = array(v)
	}
	expect := obj(core["expect"])
	requests := []any{}
	for _, name := range array(expect["requests"]) {
		requests = append(requests, obj(binding["requests"])[str(name)])
	}
	execution := merge(core["execution"], binding["execution"])
	scenario := object{"provider": o.Provider, "storyId": story["id"], "caseId": o.Case, "driverProfile": core["driverProfile"], "mode": execution["mode"], "reason": execution["reason"], "prompts": prompts, "files": merge(obj(core["setup"])["files"], binding["files"]), "answers": merge(expect["answers"], binding["answers"]), "configuration": obj(binding["configuration"]), "parameters": merge(core["parameters"], binding["parameters"]), "requiredRequests": requests, "case": object{"id": o.Case, "storyId": story["id"], "scope": expect["scope"], "outOfScope": expect["outOfScope"]}}
	return object{"provider": o.Provider, "story": story["id"], "storyId": story["id"], "caseId": o.Case, "case": scenario["case"], "driverProfile": core["driverProfile"], "prompts": prompts, "scenario": scenario, "model": o.Model}, nil
}

func copyFile(source, target string) error {
	b, e := os.ReadFile(source)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return e
	}
	return os.WriteFile(target, b, 0600)
}
func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(source, p)
		if e != nil {
			return e
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("material is not a regular file: %s", p)
		}
		return copyFile(p, dest)
	})
}
func writeFiles(root string, files object) error {
	for name, value := range files {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("material path escapes workspace: %s", name)
		}
		path := filepath.Join(root, name)
		if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return e
		}
		if e := os.WriteFile(path, []byte(str(value)), 0600); e != nil {
			return e
		}
	}
	return nil
}

// Copy only saved native records, never credentials or personal configuration.
func copySaved(source, target string) error {
	for _, name := range []string{"config/projects", "config/sessions", "history-seed"} {
		p := filepath.Join(source, name)
		if _, e := os.Stat(p); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return e
		}
		if e := copyTree(p, filepath.Join(target, name)); e != nil {
			return e
		}
	}
	return nil
}
