package lifecycle

import (
	"errors"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func artifactForMaterial(root, provenance, action string, connections []string) (*corpus.Material, error) {
	if provenance == "builtin" {
		if _, err := builtin.CheckRoot(root); err != nil {
			return nil, err
		}
	}
	material, err := corpus.ReadMaterial(root, provenance)
	if err != nil {
		return nil, err
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	material.ID, material.Action, material.CreatedAt = id, action, now()
	material.Connections = append([]string(nil), connections...)
	return material, nil
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
func LatestArtifact(sessionDir string, session *Session) (*corpus.Material, error) {
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
		if _, err := inspectMaterial(session, artifact); err != nil {
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
