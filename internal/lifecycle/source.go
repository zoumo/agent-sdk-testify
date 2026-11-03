package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type PrepareOptions struct {
	SessionDir    string
	Agent         string
	Case          string
	Plan          string
	Recording     string
	Model         string
	InitialModel  string
	HistorySeed   string
	ProtocolBytes int64
	StderrBytes   int64
	MaxFrames     int64
	MaxFrameBytes int64
}

type sourceSpec struct {
	source     Source
	plan       tool.Object
	planBytes  []byte
	provenance string
	routes     map[string]bool
}

func resolveSource(o PrepareOptions) (*sourceSpec, error) {
	count := 0
	for _, value := range []string{o.Case, o.Plan, o.Recording} {
		if value != "" {
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("prepare requires exactly one of --case, --plan or --recording")
	}
	switch {
	case o.Case != "":
		return resolveCase(o)
	case o.Plan != "":
		return resolvePlan(o)
	default:
		return resolveRecording(o)
	}
}

func validateAgent(agent string) error {
	if agent != "codex" && agent != "claude" {
		return fmt.Errorf("unsupported agent type %q: expected codex or claude", agent)
	}
	return nil
}

func caseBindings(id string) ([]string, error) {
	var found []string
	for _, provider := range []string{"codex", "claude"} {
		raw, err := fs.ReadFile(data.Scenarios(), "bindings/"+provider+".json")
		if err != nil {
			return nil, err
		}
		var binding tool.Object
		if err := json.Unmarshal(raw, &binding); err != nil {
			return nil, err
		}
		if _, ok := tool.Obj(binding["cases"])[id]; ok {
			found = append(found, provider)
		}
	}
	return found, nil
}

func resolveCase(o PrepareOptions) (*sourceSpec, error) {
	agent := o.Agent
	if agent == "" {
		bindings, err := caseBindings(o.Case)
		if err != nil {
			return nil, err
		}
		if len(bindings) != 1 {
			return nil, fmt.Errorf("case %s has %d agent bindings; specify --agent", o.Case, len(bindings))
		}
		agent = bindings[0]
	}
	if err := validateAgent(agent); err != nil {
		return nil, err
	}
	plan, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: agent, Case: o.Case, Model: o.Model})
	if err != nil {
		return nil, err
	}
	if o.InitialModel != "" {
		plan["initialModel"] = o.InitialModel
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	source := Source{
		Kind:          "case",
		ID:            o.Case,
		SHA256:        digestBytes(raw),
		Provenance:    "official",
		DriverProfile: tool.Str(plan["driverProfile"]),
	}
	return &sourceSpec{source: source, plan: plan, planBytes: raw, provenance: "official", routes: map[string]bool{"record": true, "verify": true, "mock": true}}, nil
}

func resolvePlan(o PrepareOptions) (*sourceSpec, error) {
	if !filepath.IsAbs(o.Plan) {
		return nil, errors.New("--plan must be an absolute path")
	}
	path, err := filepath.EvalSymlinks(o.Plan)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("consumer plan must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var plan tool.Object
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	if err := validateConsumerPlan(plan); err != nil {
		return nil, err
	}
	agent := tool.Str(plan["agentType"])
	if o.Agent != "" && o.Agent != agent {
		return nil, errors.New("--agent differs from plan agentType")
	}
	if o.Model != "" {
		return nil, errors.New("--model belongs to the consumer plan")
	}
	if o.InitialModel != "" {
		return nil, errors.New("initial model belongs to official case recordings")
	}
	source := Source{
		Kind:          "plan",
		ID:            tool.Str(plan["caseId"]),
		Path:          path,
		SHA256:        digestBytes(raw),
		Provenance:    "consumer",
		DriverProfile: "consumer",
	}
	return &sourceSpec{source: source, plan: plan, planBytes: raw, provenance: "consumer", routes: map[string]bool{"record": true, "verify": false, "mock": true}}, nil
}

func validateConsumerPlan(plan tool.Object) error {
	version, ok := plan["version"].(float64)
	if !ok || version != 1 {
		return errors.New("consumer plan version must be 1")
	}
	agent := tool.Str(plan["agentType"])
	if err := validateAgent(agent); err != nil {
		return err
	}
	if strings.TrimSpace(tool.Str(plan["caseId"])) == "" {
		return errors.New("consumer plan caseId is required")
	}
	if tool.Str(plan["driverProfile"]) != "consumer" {
		return errors.New("consumer plan driverProfile must be consumer")
	}
	project := tool.Str(plan["project"])
	if !filepath.IsAbs(project) {
		return errors.New("consumer plan project must be absolute")
	}
	if value, exists := plan["model"]; exists {
		if _, ok := value.(string); !ok {
			return errors.New("consumer plan model must be a string")
		}
	}
	if value, exists := plan["cliVersion"]; exists {
		if _, ok := value.(string); !ok {
			return errors.New("consumer plan cliVersion must be a string")
		}
	}
	if scenario, exists := plan["scenario"]; exists {
		scenarioObject, ok := scenario.(map[string]any)
		if !ok {
			return errors.New("consumer plan scenario must be an object")
		}
		if files, exists := scenarioObject["files"]; exists {
			fileObject, ok := files.(map[string]any)
			if !ok {
				return errors.New("consumer plan scenario.files must be an object")
			}
			for name, value := range fileObject {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("consumer plan fixture %s must be text", name)
				}
				if name == "" || !filepath.IsLocal(filepath.FromSlash(name)) || strings.Contains(name, "\\") {
					return fmt.Errorf("consumer plan fixture path is not local: %q", name)
				}
			}
		}
	}
	return nil
}

func resolveRecording(o PrepareOptions) (*sourceSpec, error) {
	if !filepath.IsAbs(o.Recording) {
		return nil, errors.New("--recording must be an absolute path")
	}
	if o.Model != "" || o.InitialModel != "" {
		return nil, errors.New("model selection belongs to the recording source")
	}
	path, err := filepath.EvalSymlinks(o.Recording)
	if err != nil {
		return nil, err
	}
	if err := corpus.VerifyMaterials(path, true); err != nil {
		return nil, err
	}
	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(path, "plan.json"), &plan); err != nil {
		return nil, err
	}
	var manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(path, "manifest.json"), &manifest); err != nil {
		return nil, err
	}
	agent := tool.Str(plan["provider"])
	if agent == "" {
		agent = tool.Str(plan["agentType"])
	}
	if agent == "" {
		return nil, errors.New("recording does not identify an agent type")
	}
	if err := validateAgent(agent); err != nil {
		return nil, err
	}
	if o.Agent != "" && o.Agent != agent {
		return nil, errors.New("--agent differs from recording agent type")
	}
	provenance := tool.Str(manifest["provenance"])
	if provenance == "" {
		provenance = "external"
	}
	planHash, err := digestFile(filepath.Join(path, "plan.json"))
	if err != nil {
		return nil, err
	}
	manifestHash, err := digestFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return nil, err
	}
	sourceHash := digestBytes([]byte(planHash + "\n" + manifestHash + "\n"))
	source := Source{
		Kind:          "recording",
		ID:            tool.Str(plan["caseId"]),
		Path:          path,
		SHA256:        sourceHash,
		Provenance:    provenance,
		DriverProfile: tool.Str(plan["driverProfile"]),
	}
	verify := provenance != "consumer" && provenance != "synthetic"
	return &sourceSpec{source: source, plan: plan, provenance: provenance, routes: map[string]bool{"record": false, "verify": verify, "mock": true}}, nil
}

func planFiles(plan tool.Object) (tool.Object, error) {
	files := tool.Obj(tool.Obj(plan["scenario"])["files"])
	for name, value := range files {
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("fixture %s must be text", name)
		}
		if name == "" || !filepath.IsLocal(filepath.FromSlash(name)) || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("fixture path is not local: %q", name)
		}
	}
	return files, nil
}

func planProtocolCwd(plan tool.Object, workspace string) string {
	if tool.Str(plan["provider"]) == "claude" || tool.Str(plan["agentType"]) == "claude" {
		return workspace
	}
	if project := tool.Str(plan["project"]); project != "" {
		return project
	}
	return workspace
}

func planModel(plan tool.Object) string {
	return tool.Str(plan["model"])
}
