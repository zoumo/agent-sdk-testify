package mock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

// JSON values never pass through float64 maps. Original bytes remain available
// for exact protected-content assertions independently of JSON whitespace.
type nativeDocument struct {
	ID       string            `json:"id"`
	Format   string            `json:"format"`
	SHA256   string            `json:"sha256"`
	Original []byte            `json:"originalBase64"`
	Value    json.RawMessage   `json:"value,omitempty"`
	Rows     []json.RawMessage `json:"rows,omitempty"`
}
type nativeConnection struct {
	ID       string            `json:"id"`
	Format   string            `json:"format"`
	Version  int               `json:"version"`
	SHA256   string            `json:"sha256"`
	Original []byte            `json:"originalBase64"`
	Frames   []json.RawMessage `json:"frames"`
}
type assertionInputs struct {
	SchemaVersion        int                `json:"schemaVersion"`
	Provider             string             `json:"provider"`
	CaseID               string             `json:"caseId"`
	Attempt              string             `json:"attempt"`
	ManifestSHA256       string             `json:"manifestSHA256,omitempty"`
	SourceManifestSHA256 string             `json:"sourceManifestSHA256"`
	MissingEvidence      []string           `json:"missingEvidence"`
	SourcePlan           nativeDocument     `json:"sourcePlan"`
	SourceReport         *nativeDocument    `json:"sourceReport,omitempty"`
	SavedHistory         []nativeDocument   `json:"savedHistory"`
	Connections          []nativeConnection `json:"connections"`
}

func nativeRows(data []byte) ([]json.RawMessage, error) {
	rows := []json.RawMessage{}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, errors.New("invalid vendor-native JSON row")
		}
		rows = append(rows, json.RawMessage(line))
	}
	return rows, nil
}

func nativeSourceDocument(root, name, id string) (nativeDocument, error) {
	source, err := corpus.Regular(root, name)
	if err != nil {
		return nativeDocument{}, err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nativeDocument{}, err
	}
	d := nativeDocument{ID: id, SHA256: tool.DigestBytes(data), Original: data, Format: "vendor-native-json"}
	if strings.HasSuffix(name, ".jsonl") {
		d.Format = "vendor-native-jsonl"
		d.Rows, err = nativeRows(data)
	} else if json.Valid(data) {
		d.Value = json.RawMessage(data)
	} else {
		return d, fmt.Errorf("invalid source JSON: %s", id)
	}
	return d, err
}

func builtinAssertionInputs(material *builtin.Material, manifestHash string) (inputs *assertionInputs, err error) {
	root, err := builtin.Extract(material)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	return sourceAssertionInputs(root, material, manifestHash, true)
}

func sourceAssertionInputs(root string, material *builtin.Material, manifestHash string, requireReport bool) (inputs *assertionInputs, err error) {
	inputs = &assertionInputs{SchemaVersion: 1, Provider: material.Provider, CaseID: material.CaseID, Attempt: material.Attempt, ManifestSHA256: manifestHash, SavedHistory: []nativeDocument{}, Connections: []nativeConnection{}, MissingEvidence: []string{}}
	inputs.SourceManifestSHA256, err = tool.FileDigest(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	inputs.SourcePlan, err = nativeSourceDocument(root, "plan.json", "source-plan")
	if err == nil && inputs.SourcePlan.SHA256 != material.Files["plan.json"] {
		err = errors.New("source plan hash drift")
	}
	if err != nil {
		return nil, err
	}
	report, e := nativeSourceDocument(root, "report.json", "source-report")
	if os.IsNotExist(e) && !requireReport {
		inputs.MissingEvidence = append(inputs.MissingEvidence, "sourceReport")
	} else if e != nil {
		return nil, e
	} else {
		if expected := material.Files["report.json"]; expected != nil && report.SHA256 != expected {
			return nil, errors.New("source report hash drift")
		}
		inputs.SourceReport = &report
	}
	for _, phase := range []struct{ id, name string }{{"main", "raw/raw.jsonl"}, {"probe", "remote-probe/raw/raw.jsonl"}} {
		if material.Files[phase.name] == nil {
			continue
		}
		data, e := os.ReadFile(filepath.Join(root, phase.name))
		if e != nil {
			return nil, e
		}
		if tool.DigestBytes(data) != material.Files[phase.name] {
			return nil, errors.New("source connection hash drift")
		}
		frames, e := nativeRows(data)
		if e != nil {
			return nil, e
		}
		inputs.Connections = append(inputs.Connections, nativeConnection{ID: phase.id, Format: "agent-mock-jsonl", Version: 1, SHA256: tool.DigestBytes(data), Original: data, Frames: frames})
	}
	for _, name := range corpus.Keys(material.Files) {
		if !strings.HasPrefix(name, "config/projects/") && !strings.HasPrefix(name, "config/sessions/") && !strings.HasPrefix(name, "history-seed/") {
			continue
		}
		// Opaque source labels associate frozen seed hashes; never consumer paths.
		d, e := nativeSourceDocument(root, name, "saved-history:"+name)
		if e != nil {
			return nil, e
		}
		if d.SHA256 != material.Files[name] {
			return nil, errors.New("source history hash drift")
		}
		inputs.SavedHistory = append(inputs.SavedHistory, d)
	}
	return inputs, nil
}

func writeNativeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

// External descriptions are source-bound inputs, never catalog/admission claims.
func describeExternalMock(root, provider string) error {
	if !filepath.IsAbs(root) {
		return errors.New("external describe requires --recording ABS")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	manifest, plan, document, err := loadMockSource(root, provider)
	if err != nil {
		return err
	}
	provider = planAgentType(plan)
	files := tool.Object{"raw/raw.jsonl": document.SHA256, "plan.json": manifest["planSHA256"]}
	for _, group := range []string{"savedFiles", "caseFiles", "remotePhaseFiles"} {
		maps.Copy(files, tool.Obj(manifest[group]))
	}
	for name, value := range tool.Obj(tool.Obj(plan["historySeed"])["files"]) {
		hash := tool.Str(tool.Obj(value)["sha256"])
		if err := corpus.VerifyFile(root, name, hash); err != nil {
			return err
		}
		files[name] = hash
	}
	if _, e := os.Stat(filepath.Join(root, "adoption.json")); e == nil {
		review, reviewErr := corpus.VerifyAdoptedFiles(root)
		if reviewErr != nil {
			return reviewErr
		}
		for _, name := range tool.Strings(review["publishFiles"]) {
			files[name] = tool.Obj(review["captureFiles"])[name]
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	if plan["driverProfile"] == "remote-close" {
		_, _, probe, e := loadMockPhase(root, provider, "probe")
		if e != nil {
			return e
		}
		files["remote-probe/raw/raw.jsonl"] = probe.SHA256
	}
	planBytes, err := os.ReadFile(filepath.Join(root, "plan.json"))
	if err != nil {
		return err
	}
	manifestHash, err := tool.FileDigest(filepath.Join(root, "manifest.json"))
	if err != nil {
		return err
	}
	material := &builtin.Material{Provider: provider, CaseID: tool.Str(plan["caseId"]), StoryID: tool.Str(plan["storyId"]), Attempt: "external:" + manifestHash, Files: files, Plan: planBytes}
	inputs, err := sourceAssertionInputs(root, material, "", false)
	if err != nil {
		return err
	}
	// Recheck immutable source bindings after reading all assertion data.
	if _, _, _, err = loadMockSource(root, provider); err != nil {
		return err
	}
	currentHash, e := tool.FileDigest(filepath.Join(root, "manifest.json"))
	if e != nil {
		return e
	}
	if currentHash != manifestHash || inputs.SourceManifestSHA256 != manifestHash {
		return errors.New("source manifest hash drift")
	}
	provenance := tool.Str(manifest["provenance"])
	if provenance == "" {
		provenance = "unqualified"
	}
	return writeNativeJSON(tool.Object{"schemaVersion": 1, "toolVersion": version.Version, "materialKind": "external", "qualification": "unqualified", "provenance": provenance, "provider": provider, "caseId": material.CaseID, "sourceManifestSHA256": manifestHash, "available": true, "plan": json.RawMessage(planBytes), "assertionInputs": inputs})
}
