// Package catalog projects the authoritative story and provider bindings into
// one run plan. It does not embed or discover a source checkout.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type Selection struct {
	Provider string
	Case     string
	Model    string
}

// Resolve loads an existing plan or projects a catalog case. Shared catalog
// inputs supply identities and prompts; runtime paths remain attempt-local.
type ResolveInput struct {
	Mode      string
	Materials string
	Case      string
	Provider  string
	Model     string
}

func Resolve(catalog fs.FS, input ResolveInput) (tool.Object, error) {
	if input.Mode == "record" && input.Materials == "" {
		return Load(catalog, Selection{Provider: input.Provider, Case: input.Case, Model: input.Model})
	}
	if err := corpus.VerifyMaterials(input.Materials, input.Mode != "record"); err != nil {
		return nil, err
	}
	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(input.Materials, "plan.json"), &plan); err == nil {
		if input.Case != "" && input.Case != tool.Str(plan["caseId"]) {
			return nil, errors.New("case differs from selected recording")
		}
		if input.Provider != "" && input.Provider != tool.Str(plan["provider"]) {
			return nil, errors.New("provider differs from selected recording")
		}
		return plan, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := tool.ReadJSON(filepath.Join(input.Materials, "case.json"), &plan); err == nil {
		return plan, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return Load(os.DirFS(input.Materials), Selection{Provider: input.Provider, Case: input.Case, Model: input.Model})
}

func Load(catalog fs.FS, selection Selection) (tool.Object, error) {
	paths, err := fs.Glob(catalog, "SDK-US-*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	read := func(path string, v any) error {
		b, err := fs.ReadFile(catalog, path)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, v)
	}
	var core, story tool.Object
	for _, path := range paths {
		var s tool.Object
		if err := read(path, &s); err != nil {
			return nil, err
		}
		for _, c := range tool.Array(s["cases"]) {
			if tool.Str(tool.Obj(c)["id"]) == selection.Case {
				core = tool.Obj(c)
				story = s
			}
		}
	}
	if core == nil {
		return nil, fmt.Errorf("case %q is absent from shared materials", selection.Case)
	}
	var bindings tool.Object
	if err := read("bindings/"+selection.Provider+".json", &bindings); err != nil {
		return nil, err
	}
	rawBinding, ok := tool.Obj(bindings["cases"])[selection.Case]
	if !ok {
		return nil, errors.New("provider has no material binding for case")
	}
	binding := tool.Obj(rawBinding)
	merge := func(a, b any) tool.Object {
		out := tool.Object{}
		maps.Copy(out, tool.Obj(a))
		maps.Copy(out, tool.Obj(b))
		return out
	}
	inputs := tool.Obj(core["inputs"])
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	prompts := []any{}
	for _, key := range names {
		prompts = append(prompts, inputs[key])
	}
	if value, ok := binding["inputs"]; ok {
		prompts = tool.Array(value)
	}
	expect := tool.Obj(core["expect"])
	requests := []any{}
	for _, name := range tool.Array(expect["requests"]) {
		requests = append(requests, tool.Obj(binding["requests"])[tool.Str(name)])
	}
	execution := merge(core["execution"], binding["execution"])
	scenario := tool.Object{
		"provider":         selection.Provider,
		"storyId":          story["id"],
		"caseId":           selection.Case,
		"driverProfile":    core["driverProfile"],
		"mode":             execution["mode"],
		"reason":           execution["reason"],
		"prompts":          prompts,
		"files":            merge(tool.Obj(core["setup"])["files"], binding["files"]),
		"answers":          merge(expect["answers"], binding["answers"]),
		"configuration":    tool.Obj(binding["configuration"]),
		"parameters":       merge(core["parameters"], binding["parameters"]),
		"requiredRequests": requests,
		"case": tool.Object{
			"id":         selection.Case,
			"storyId":    story["id"],
			"scope":      expect["scope"],
			"outOfScope": expect["outOfScope"],
		},
	}
	return tool.Object{
		"provider":      selection.Provider,
		"story":         story["id"],
		"storyId":       story["id"],
		"caseId":        selection.Case,
		"case":          scenario["case"],
		"driverProfile": core["driverProfile"],
		"prompts":       prompts,
		"scenario":      scenario,
		"model":         selection.Model,
	}, nil
}

// CheckVerifierPlan requires the exact official case and agent binding. Runtime
// paths and native launch options are deliberately outside the case contract.
func CheckVerifierPlan(plan, bound tool.Object) error {
	if tool.Str(tool.Obj(bound["scenario"])["mode"]) != "live-selected-path" {
		return errors.New("official case binding has no applicable verifier")
	}
	for _, key := range []string{"provider", "storyId", "caseId", "driverProfile", "case", "prompts", "scenario"} {
		if !reflect.DeepEqual(plan[key], bound[key]) {
			return fmt.Errorf("recording %s differs from official case binding", key)
		}
	}
	return nil
}
