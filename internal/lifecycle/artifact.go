package lifecycle

import (
	"errors"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func artifactForMaterial(root, provenance string, connections []string) (*corpus.Material, error) {
	hasOfficialOrigin := false
	if provenance == "builtin" {
		selected, err := builtin.CheckRoot(root)
		if err != nil {
			return nil, err
		}
		hasOfficialOrigin = selected.Files["official-origin.json"] != nil
	}
	material, err := corpus.ReadMaterial(root, provenance)
	if err != nil {
		return nil, err
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	material.ID, material.Action, material.CreatedAt = id, "initial", now()
	material.FormatVersion = corpus.MaterialFormatVersion
	material.Connections = append([]string(nil), connections...)
	if hasOfficialOrigin {
		var origin corpus.OfficialOrigin
		if err := tool.ReadJSON(filepath.Join(root, "official-origin.json"), &origin); err != nil {
			return nil, err
		}
		material.OfficialOrigin = &origin
		if err := corpus.ValidateOfficialCapture(*material); err != nil {
			return nil, err
		}
	}
	return material, nil
}

func LoadPlan(fixture *Fixture) (tool.Object, error) {
	path := fixture.Source.Path
	switch fixture.Source.Kind {
	case "case", "plan":
	case "recording":
		path = filepath.Join(fixture.Source.Path, "plan.json")
	default:
		return nil, errors.New("unknown fixture source")
	}
	if fixture.Source.Kind == "case" || fixture.Source.Kind == "plan" {
		hash, err := tool.FileDigest(path)
		if err != nil {
			return nil, err
		}
		if hash != fixture.Source.SHA256 {
			return nil, errors.New("fixture source changed after prepare")
		}
	} else {
		planHash, err := tool.FileDigest(path)
		if err != nil {
			return nil, err
		}
		manifestHash, err := tool.FileDigest(filepath.Join(fixture.Source.Path, "manifest.json"))
		if err != nil {
			return nil, err
		}
		if tool.DigestBytes([]byte(planHash+"\n"+manifestHash+"\n")) != fixture.Source.SHA256 {
			return nil, errors.New("fixture source changed after prepare")
		}
	}
	var plan tool.Object
	if err := tool.ReadJSON(path, &plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// CurrentArtifact resolves the single current recording. A fixture without a
// current recording is a valid prepared state and returns present=false.
func CurrentArtifact(fixtureDir string, fixture *Fixture) (*corpus.Material, bool, error) {
	dir, err := CanonicalFixtureDir(fixtureDir)
	if err != nil {
		return nil, false, err
	}
	fixtureDir = dir
	current, err := readCurrent(fixtureDir, fixture.FixtureID)
	if err != nil {
		return nil, false, err
	}
	if current.MaterialID == "" {
		return nil, false, nil
	}
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return nil, false, err
	}
	for _, artifact := range index.Artifacts {
		if artifact.ID != current.MaterialID {
			continue
		}
		if _, err := inspectMaterial(fixture, artifact); err != nil {
			return nil, false, err
		}
		return &artifact, true, nil
	}
	return nil, false, errors.New("current recording is absent from material index")
}

func readCurrent(fixtureDir, fixtureID string) (Current, error) {
	path := filepath.Join(fixtureDir, "current.json")
	if err := persistedSchema(path, "current recording"); err != nil {
		return Current{}, err
	}
	var current Current
	if err := tool.ReadJSON(path, &current); err != nil {
		return Current{}, err
	}
	if current.FixtureID != fixtureID {
		return Current{}, errors.New("invalid current recording identity")
	}
	return current, nil
}

// LatestArtifact resolves the single current recording.
func LatestArtifact(fixtureDir string, fixture *Fixture) (*corpus.Material, error) {
	material, present, err := CurrentArtifact(fixtureDir, fixture)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("fixture has no current recording")
	}
	return material, nil
}

type Current struct {
	SchemaVersion int    `json:"schemaVersion"`
	FixtureID     string `json:"fixtureId"`
	MaterialID    string `json:"materialId,omitempty"`
}

func writeCurrent(fixtureDir string, fixture *Fixture, materialID string) error {
	return writeAtomic(filepath.Join(fixtureDir, "current.json"), Current{SchemaVersion: SchemaVersion, FixtureID: fixture.FixtureID, MaterialID: materialID})
}
