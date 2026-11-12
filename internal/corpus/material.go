package corpus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// MaterialFormatVersion is the portable material descriptor version. It stores
// identity, provenance, hashes and material-root-relative bindings without a
// machine-local location.
const MaterialFormatVersion = 2

type Material struct {
	FormatVersion   int               `json:"formatVersion,omitempty"`
	OfficialOrigin  *OfficialOrigin   `json:"officialOrigin,omitempty"`
	Files           map[string]string `json:"files,omitempty"`
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Action          string            `json:"action"`
	Provenance      string            `json:"provenance"`
	Path            string            `json:"-"`
	PlanSHA256      string            `json:"planSHA256"`
	ManifestSHA256  string            `json:"manifestSHA256"`
	RecordingSHA256 string            `json:"recordingSHA256"`
	Connections     []string          `json:"connections"`
	CreatedAt       string            `json:"createdAt"`
}

type materialWire struct {
	FormatVersion   int               `json:"formatVersion,omitempty"`
	OfficialOrigin  *OfficialOrigin   `json:"officialOrigin,omitempty"`
	Files           map[string]string `json:"files,omitempty"`
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Action          string            `json:"action"`
	Provenance      string            `json:"provenance"`
	Path            string            `json:"path,omitempty"`
	PlanSHA256      string            `json:"planSHA256"`
	ManifestSHA256  string            `json:"manifestSHA256"`
	RecordingSHA256 string            `json:"recordingSHA256"`
	Connections     []string          `json:"connections"`
	CreatedAt       string            `json:"createdAt"`
}

func (m *Material) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire materialWire
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.FormatVersion != 0 && wire.FormatVersion != MaterialFormatVersion {
		return fmt.Errorf("unsupported material formatVersion %d; format %d is required", wire.FormatVersion, MaterialFormatVersion)
	}
	if wire.FormatVersion == MaterialFormatVersion && wire.Path != "" {
		return errors.New("format 2 material declares a machine-local path")
	}
	*m = Material{
		FormatVersion: wire.FormatVersion, OfficialOrigin: wire.OfficialOrigin,
		Files: wire.Files, ID: wire.ID, Kind: wire.Kind, Action: wire.Action,
		Provenance: wire.Provenance, Path: wire.Path,
		PlanSHA256: wire.PlanSHA256, ManifestSHA256: wire.ManifestSHA256,
		RecordingSHA256: wire.RecordingSHA256, Connections: wire.Connections,
		CreatedAt: wire.CreatedAt,
	}
	return nil
}

// MarshalJSON never persists a machine-local material location.
func (m Material) MarshalJSON() ([]byte, error) {
	type materialOutput Material
	return json.Marshal(materialOutput(m))
}

// OfficialOrigin identifies capture ownership, not a business pass. It is sealed
// into the material before publication; the later run result is separate evidence.
type OfficialOrigin struct {
	RunID              string   `json:"runId"`
	FixtureID          string   `json:"fixtureId"`
	CaseID             string   `json:"caseId"`
	AgentType          string   `json:"agentType"`
	RecordSourceSHA256 string   `json:"recordSourceSHA256"`
	SourceSHA256       string   `json:"sourceSHA256"`
	DriverSHA256       string   `json:"driverSHA256"`
	Connections        []string `json:"connections"`
}

// UnmarshalJSON keeps immutable repository evidence readable after the
// tool-owned identity moved from sessionId to fixtureId. MarshalJSON remains
// the default struct encoding and therefore never writes the old field.
func (o *OfficialOrigin) UnmarshalJSON(data []byte) error {
	type wire OfficialOrigin
	var decoded struct {
		*wire
		LegacyFixtureID string `json:"sessionId"`
	}
	decoded.wire = (*wire)(o)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if o.FixtureID == "" {
		o.FixtureID = decoded.LegacyFixtureID
	}
	return nil
}

// ValidateOfficialCapture checks portable, sealed capture ownership. It does
// not read the originating fixture or imply that the parent's business passed.
func ValidateOfficialCapture(artifact Material) error {
	origin := artifact.OfficialOrigin
	if origin == nil || origin.RunID == "" || origin.FixtureID == "" || len(origin.DriverSHA256) != 64 {
		return errors.New("official capture origin is incomplete")
	}
	var sealed OfficialOrigin
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "official-origin.json"), &sealed); err != nil {
		return err
	}
	if !reflect.DeepEqual(sealed, *origin) || !slices.Equal(origin.Connections, artifact.Connections) {
		return errors.New("sealed official capture origin differs from material")
	}
	digest, err := tool.FileDigest(filepath.Join(artifact.Path, "source-plan.json"))
	if err != nil || digest != origin.SourceSHA256 {
		return errors.New("official capture lost its original plan")
	}
	var manifest, plan tool.Object
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "manifest.json"), &manifest); err != nil {
		return err
	}
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "plan.json"), &plan); err != nil {
		return err
	}
	if manifest["sourcePlanSHA256"] != digest || plan["sourcePlanSHA256"] != digest || plan["caseId"] != origin.CaseID || plan["provider"] != origin.AgentType {
		return errors.New("official derived plan lost its source binding")
	}
	executionID := ""
	for _, id := range origin.Connections {
		if id == "" || filepath.Base(id) != id || !filepath.IsLocal(id) {
			return errors.New("invalid official connection identity")
		}
		var proof struct {
			ExecutionID   string         `json:"executionId"`
			Origin        OfficialOrigin `json:"origin"`
			ConnectionID  string         `json:"connectionId"`
			ProtocolPID   int            `json:"protocolPid"`
			SupervisorPID int            `json:"supervisorPid"`
			ToolSHA256    string         `json:"toolSHA256"`
		}
		if err := tool.ReadJSON(filepath.Join(artifact.Path, "origin", id+".json"), &proof); err != nil {
			return err
		}
		if proof.ExecutionID == "" || (executionID != "" && proof.ExecutionID != executionID) {
			return errors.New("official launch proofs disagree on execution")
		}
		executionID = proof.ExecutionID
		if !reflect.DeepEqual(proof.Origin, *origin) || proof.ConnectionID != id || proof.ProtocolPID <= 0 || proof.SupervisorPID <= 0 || len(proof.ToolSHA256) != 64 {
			return errors.New("official connection launch proof is incomplete")
		}
	}
	return nil
}

type MaterialInspection struct {
	Plan         tool.Object
	Manifest     tool.Object
	Provenance   string
	HistoryFiles map[string]string
}

func SelectionProvenance(plan, manifest tool.Object) (string, error) {
	value := tool.Str(manifest["provenance"])
	consumerProfile := tool.Str(plan["driverProfile"]) == "consumer"
	switch value {
	case "consumer":
		if !consumerProfile {
			return "", errors.New("consumer material provenance conflicts with the plan")
		}
		return "consumer", nil
	case "official":
		if consumerProfile {
			return "", errors.New("official material provenance conflicts with a consumer plan")
		}
		return "official", nil
	case "builtin":
		if consumerProfile {
			return "", errors.New("builtin material provenance conflicts with a consumer plan")
		}
		return "builtin", nil
	case "external", "synthetic":
		if consumerProfile {
			return "", errors.New("external material provenance conflicts with a consumer plan")
		}
		return "external", nil
	case "capture":
		if consumerProfile {
			return "", errors.New("capture provenance conflicts with a consumer plan")
		}
		// A capture manifest alone does not establish official ownership;
		// the sealed material descriptor carries that separate evidence.
		return "external", nil
	default:
		return "", fmt.Errorf("material provenance is missing or unknown: %q", value)
	}
}

func InspectMaterial(artifact Material) (*MaterialInspection, error) {
	return inspectMaterial(artifact, true)
}

// InspectMaterialWithoutDescriptor validates a supported legacy package whose
// identity is stored only in the caller's fixture index. The material
// directory must not gain a descriptor during the import.
func InspectMaterialWithoutDescriptor(artifact Material) (*MaterialInspection, error) {
	return inspectMaterial(artifact, false)
}

func inspectMaterial(artifact Material, descriptor bool) (*MaterialInspection, error) {
	if artifact.Path == "" || !filepath.IsAbs(artifact.Path) {
		return nil, errors.New("material path must be absolute")
	}
	info, err := os.Lstat(artifact.Path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("material path is not an unlinked directory")
	}
	if len(artifact.Files) > 0 {
		var err error
		if descriptor {
			err = CheckMaterialInventory(artifact.Path, artifact)
		} else {
			err = CheckMaterialFiles(artifact.Path, artifact)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := VerifyMaterials(artifact.Path, true); err != nil {
		return nil, err
	}
	var plan, manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "plan.json"), &plan); err != nil {
		return nil, err
	}
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "manifest.json"), &manifest); err != nil {
		return nil, err
	}
	selected, err := SelectionProvenance(plan, manifest)
	if err != nil {
		return nil, err
	}
	if err := checkMaterialProvenance(artifact, plan, manifest, selected); err != nil {
		return nil, err
	}
	selected = artifact.Provenance
	historyFiles, err := history.Bindings(artifact.Path)
	if err != nil {
		return nil, err
	}
	for name, expected := range map[string]string{
		"plan.json":     artifact.PlanSHA256,
		"manifest.json": artifact.ManifestSHA256,
		"raw/raw.jsonl": artifact.RecordingSHA256,
	} {
		if expected == "" {
			return nil, fmt.Errorf("artifact omits %s hash", name)
		}
		actual, err := tool.FileDigest(filepath.Join(artifact.Path, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, fmt.Errorf("artifact hash mismatch: %s", name)
		}
	}
	return &MaterialInspection{Plan: plan, Manifest: manifest, Provenance: selected, HistoryFiles: historyFiles}, nil
}

func CheckMaterialInventory(root string, artifact Material) error {
	var descriptor Material
	if err := tool.ReadJSON(filepath.Join(root, "material.json"), &descriptor); err != nil {
		return err
	}
	if !artifact.SameDescriptor(descriptor) {
		return errors.New("material descriptor differs from its index")
	}
	actual, err := history.HashTree(root)
	if err != nil {
		return err
	}
	delete(actual, "material.json")
	if !reflect.DeepEqual(actual, artifact.Files) {
		return errors.New("material closure hash mismatch")
	}
	return nil
}

// CheckMaterialFiles verifies a descriptorless legacy package against the
// identity and hashes held by the current fixture index.
func CheckMaterialFiles(root string, artifact Material) error {
	if _, err := os.Lstat(filepath.Join(root, "material.json")); err == nil {
		return errors.New("descriptorless material contains material.json")
	} else if !os.IsNotExist(err) {
		return err
	}
	actual, err := history.HashTree(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, artifact.Files) {
		return errors.New("material closure hash mismatch")
	}
	return nil
}

// SameDescriptor compares persisted material identity without treating the
// caller-resolved root as part of the descriptor.
func (m Material) SameDescriptor(other Material) bool {
	a, b := m, other
	a.Path, b.Path = "", ""
	return reflect.DeepEqual(a, b)
}

func checkMaterialProvenance(artifact Material, plan, manifest tool.Object, selected string) error {
	claimed := artifact.Provenance
	switch claimed {
	case "official":
		if artifact.Kind == "capture" {
			if artifact.OfficialOrigin != nil {
				if err := ValidateOfficialCapture(artifact); err != nil {
					return err
				}
			}
			if artifact.Action != "record" {
				return errors.New("official capture provenance conflicts with its action or source")
			}
			if selected != "external" || tool.Str(manifest["provenance"]) != "capture" {
				return errors.New("official capture manifest does not claim a capture")
			}
		} else if selected != "official" {
			return errors.New("official material provenance conflicts with its manifest")
		}
	case "consumer":
		if selected != "consumer" || tool.Str(plan["driverProfile"]) != "consumer" {
			return errors.New("consumer provenance conflicts with its plan or manifest")
		}
	case "external":
		if selected != "external" {
			return errors.New("external provenance conflicts with its manifest")
		}
	case "builtin":
		if selected == "consumer" {
			return errors.New("builtin provenance conflicts with consumer material")
		}

	default:
		return fmt.Errorf("artifact provenance is missing or unknown: %q", claimed)
	}
	return nil
}

// ReadMaterialDescriptor loads the descriptor owned by a supported material
// format. The caller supplies the material root explicitly; a stored legacy
// path is retained in the file bytes but is never used to locate content.
// A nil descriptor means a supported legacy package without material.json.
func ReadMaterialDescriptor(root string) (*Material, error) {
	path := filepath.Join(root, "material.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var material Material
	if err := tool.ReadJSON(path, &material); err != nil {
		return nil, err
	}
	if len(material.Files) == 0 {
		return nil, errors.New("material descriptor omits its file inventory")
	}
	material.Path = root
	if err := CheckMaterialInventory(root, material); err != nil {
		return nil, err
	}
	return &material, nil
}

// ReadMaterial describes an imported payload. Its caller assigns fixture-local
// identity and checks any builtin snapshot against the builtin contract.
func ReadMaterial(root, provenance string) (*Material, error) {
	material := &Material{Kind: "material", Path: root, Provenance: provenance}
	for name, target := range map[string]*string{
		"plan.json": &material.PlanSHA256, "manifest.json": &material.ManifestSHA256, "raw/raw.jsonl": &material.RecordingSHA256,
	} {
		digest, err := tool.FileDigest(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		*target = digest
	}
	if _, err := InspectMaterial(*material); err != nil {
		return nil, err
	}
	return material, nil
}
