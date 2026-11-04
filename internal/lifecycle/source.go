package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
	material  *corpus.Material
	source    Source
	plan      tool.Object
	planBytes []byte
	agentType string
	routes    map[string]bool
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
	_, err := agentProfileFor(agent)
	return err
}

func caseBindings(id string) ([]string, error) {
	var found []string
	for _, provider := range agentTypes() {
		profile, err := agentProfileFor(provider)
		if err != nil {
			return nil, err
		}
		raw, err := fs.ReadFile(data.Scenarios(), profile.binding)
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
		SHA256:        tool.DigestBytes(raw),
		Provenance:    "official",
		DriverProfile: tool.Str(plan["driverProfile"]),
	}
	return &sourceSpec{source: source, plan: plan, planBytes: raw, agentType: agent, routes: map[string]bool{"record": true, "mock": true}}, nil
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
	if err := catalog.CheckConsumerPlan(plan); err != nil {
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
		SHA256:        tool.DigestBytes(raw),
		Provenance:    "consumer",
		DriverProfile: "consumer",
	}
	return &sourceSpec{source: source, plan: plan, planBytes: raw, agentType: agent, routes: map[string]bool{"record": true, "mock": true}}, nil
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
	// Session/export packages carry a complete payload inventory in addition
	// to the legacy protocol manifest. Validate it before making a fresh copy.
	var material corpus.Material
	metadata := filepath.Join(path, "material.json")
	if _, err := os.Lstat(metadata); err == nil {
		if err := tool.ReadJSON(metadata, &material); err != nil {
			return nil, err
		}
		if len(material.Files) == 0 {
			return nil, errors.New("material closure hash mismatch")
		}
		if err := corpus.CheckMaterialInventory(path, material); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
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
	var rawManifest tool.Object
	if err := tool.ReadJSON(filepath.Join(path, "raw", "manifest.json"), &rawManifest); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	agent, err := corpus.RecordingAgentType(plan, manifest, rawManifest)
	if err != nil {
		return nil, err
	}
	if o.Agent != "" && o.Agent != agent {
		return nil, errors.New("--agent differs from recording agent type")
	}
	provenance, err := corpus.SelectionProvenance(plan, manifest)
	if err != nil {
		return nil, err
	}
	var imported *corpus.Material
	if len(material.Files) > 0 {
		if material.ID == "" || (material.Kind != "material" && material.Kind != "capture") {
			return nil, errors.New("invalid imported material identity")
		}
		// Capture manifests retain their original provenance. Only complete
		// metadata can carry the official identity of a session-produced capture.
		provenance = material.Provenance
		imported = &material
	}
	planHash, err := tool.FileDigest(filepath.Join(path, "plan.json"))
	if err != nil {
		return nil, err
	}
	manifestHash, err := tool.FileDigest(filepath.Join(path, "manifest.json"))
	if err != nil {
		return nil, err
	}
	sourceHash := tool.DigestBytes([]byte(planHash + "\n" + manifestHash + "\n"))
	source := Source{
		Kind:          "recording",
		ID:            tool.Str(plan["caseId"]),
		Path:          path,
		SHA256:        sourceHash,
		Provenance:    provenance,
		DriverProfile: tool.Str(plan["driverProfile"]),
	}
	return &sourceSpec{material: imported, source: source, plan: plan, agentType: agent, routes: map[string]bool{"record": recordingExecutor(plan, agent, provenance) != "", "mock": true}}, nil
}

func planProtocolCwd(plan tool.Object, workspace string) string {
	agentType := tool.Str(plan["provider"])
	if agentType == "" {
		agentType = tool.Str(plan["agentType"])
	}
	if profile, ok := agentProfiles[agentType]; ok {
		return profile.protocolCWD(plan, workspace)
	}
	return workspace
}

func planModel(plan tool.Object) string {
	return tool.Str(plan["model"])
}

// Recording uses its saved protocol plan, independent of an official SDK binding.
func recordingExecutor(plan tool.Object, agent, provenance string) string {
	if provenance == "consumer" && catalog.CheckConsumerPlan(plan) != nil {
		return ""
	}
	if agent == "codex" && !filepath.IsAbs(tool.Str(plan["project"])) {
		return ""
	}
	return "external-sdk"
}
