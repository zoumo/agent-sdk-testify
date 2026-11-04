package lifecycle

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func artifactForMaterial(root, provenance, action string, connections []string) (*Artifact, error) {
	var plan, manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "plan.json"), &plan); err != nil {
		return nil, err
	}
	if err := tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest); err != nil {
		return nil, err
	}
	if provenance == "" {
		return nil, errors.New("material provenance is missing")
	}
	if _, err := history.Bindings(root); err != nil {
		return nil, err
	}
	selected, err := selectionProvenance(plan, manifest)
	if err != nil {
		return nil, err
	}
	switch provenance {
	case "builtin":
		if _, err := builtin.CheckRoot(root); err != nil {
			return nil, err
		}
	case "official":
		if selected != "official" && tool.Str(manifest["provenance"]) != "capture" {
			return nil, errors.New("official provenance conflicts with its material manifest")
		}
	case "consumer":
		if selected != "consumer" {
			return nil, errors.New("consumer provenance conflicts with its material manifest")
		}
	case "external":
		if selected != "external" {
			return nil, errors.New("external provenance conflicts with its material manifest")
		}
	default:
		return nil, errors.New("material provenance is invalid")
	}
	planHash, err := tool.FileDigest(filepath.Join(root, "plan.json"))
	if err != nil {
		return nil, err
	}
	manifestHash, err := tool.FileDigest(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	recordingHash := ""
	if digest, err := tool.FileDigest(filepath.Join(root, "raw", "raw.jsonl")); err == nil {
		recordingHash = digest
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if recordingHash == "" {
		return nil, errors.New("material omits raw/raw.jsonl")
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	return &Artifact{
		ID:              id,
		Kind:            "material",
		Action:          action,
		Provenance:      provenance,
		Path:            root,
		PlanSHA256:      planHash,
		ManifestSHA256:  manifestHash,
		RecordingSHA256: recordingHash,
		Connections:     append([]string(nil), connections...),
		CreatedAt:       now(),
	}, nil
}

func LoadPlan(session *Session) (tool.Object, error) {
	path := session.Source.Path
	switch session.Source.Kind {
	case "case", "plan":
	case "recording":
		path = filepath.Join(session.Source.Path, "plan.json")
	default:
		return nil, errors.New("unknown session source")
	}
	if session.Source.Kind == "case" || session.Source.Kind == "plan" {
		hash, err := tool.FileDigest(path)
		if err != nil {
			return nil, err
		}
		if hash != session.Source.SHA256 {
			return nil, errors.New("session source changed after prepare")
		}
	} else {
		planHash, err := tool.FileDigest(path)
		if err != nil {
			return nil, err
		}
		manifestHash, err := tool.FileDigest(filepath.Join(session.Source.Path, "manifest.json"))
		if err != nil {
			return nil, err
		}
		if tool.DigestBytes([]byte(planHash+"\n"+manifestHash+"\n")) != session.Source.SHA256 {
			return nil, errors.New("session source changed after prepare")
		}
	}
	var plan tool.Object
	if err := tool.ReadJSON(path, &plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// LatestArtifact resolves the single current recording.
func LatestArtifact(sessionDir string, session *Session, verify bool) (*Artifact, error) {
	var current Current
	if err := tool.ReadJSON(filepath.Join(sessionDir, "current.json"), &current); err != nil {
		return nil, err
	}
	if current.SchemaVersion != SchemaVersion || current.SessionID != session.SessionID {
		return nil, errors.New("invalid current recording identity or schemaVersion")
	}
	if current.MaterialID == "" {
		return nil, errors.New("session has no current recording")
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}
	for _, artifact := range index.Artifacts {
		if artifact.ID != current.MaterialID {
			continue
		}
		if _, err := inspectMaterial(session, artifact, artifactAction(verify)); err != nil {
			return nil, err
		}
		return &artifact, nil
	}
	return nil, errors.New("current recording is absent from material index")
}

type Current struct {
	SchemaVersion int    `json:"schemaVersion"`
	SessionID     string `json:"sessionId"`
	MaterialID    string `json:"materialId,omitempty"`
}

func writeCurrent(sessionDir string, session *Session, materialID string) error {
	return writeAtomic(filepath.Join(sessionDir, "current.json"), Current{SchemaVersion: SchemaVersion, SessionID: session.SessionID, MaterialID: materialID})
}

func artifactAction(verify bool) string {
	if verify {
		return "verify"
	}
	return "mock"
}
