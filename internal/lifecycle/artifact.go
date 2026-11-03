package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

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

func checkMaterialAgent(session *Session, root string) (tool.Object, error) {
	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "plan.json"), &plan); err != nil {
		return nil, err
	}
	agentType, err := materialAgentType(plan)
	if err != nil {
		return nil, err
	}
	if agentType != session.AgentType {
		return nil, errors.New("material agent type differs from session")
	}
	if (session.Source.Kind == "case" || session.Source.Kind == "plan") && tool.Str(plan["caseId"]) != session.Source.ID {
		return nil, errors.New("material case differs from session source")
	}
	return plan, nil
}

func LatestArtifact(sessionDir string, session *Session, explicit string, verify bool) (*Artifact, error) {
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			return nil, errors.New("--recording must be an absolute path")
		}
		root, err := filepath.EvalSymlinks(explicit)
		if err != nil {
			return nil, err
		}
		if artifact, ok, err := indexedArtifactAt(sessionDir, root, verify); err != nil {
			return nil, err
		} else if ok {
			if _, err := inspectMaterial(session, *artifact, artifactAction(verify)); err != nil {
				return nil, err
			}
			return artifact, nil
		}
		plan, err := checkMaterialAgent(session, root)
		if err != nil {
			return nil, err
		}
		var manifest tool.Object
		if err := tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest); err != nil {
			return nil, err
		}
		provenance, err := selectionProvenance(plan, manifest)
		if err != nil {
			return nil, err
		}
		if verify && provenance == "consumer" {
			return nil, errors.New("consumer material cannot be verified")
		}
		connections := ConnectionIDs(session.Source, plan)
		artifact, err := artifactForMaterial(root, provenance, "external", connections)
		if err != nil {
			return nil, err
		}
		action := "mock"
		if verify {
			action = "verify"
		}
		if _, err := inspectMaterial(session, *artifact, action); err != nil {
			return nil, err
		}
		return artifact, nil
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}
	if verify {
		if index.LatestCapture == "" {
			if session.Source.Kind == "recording" {
				for _, artifact := range slices.Backward(index.Artifacts) {
					if artifact.Kind == "material" {
						if _, err := inspectMaterial(session, artifact, "verify"); err != nil {
							return nil, err
						}
						return &artifact, nil
					}
				}
			}
			return nil, errors.New("session has no record capture to verify")
		}
		artifact, err := latestCaptureArtifact(index)
		if err != nil {
			return nil, err
		}
		if _, err := inspectMaterial(session, *artifact, "verify"); err != nil {
			return nil, err
		}
		return artifact, nil
	}
	if index.LatestCapture != "" {
		artifact, err := latestCaptureArtifact(index)
		if err != nil {
			return nil, err
		}
		if _, err := inspectMaterial(session, *artifact, "mock"); err != nil {
			return nil, err
		}
		return artifact, nil
	}
	for _, artifact := range slices.Backward(index.Artifacts) {
		if artifact.Kind == "material" {
			if _, err := inspectMaterial(session, artifact, "mock"); err != nil {
				return nil, err
			}
			return &artifact, nil
		}
	}
	return nil, errors.New("no mock material or record capture is available")
}

func latestCaptureArtifact(index *Index) (*Artifact, error) {
	for i := range index.Artifacts {
		artifact := &index.Artifacts[i]
		if artifact.ID != index.LatestCapture {
			continue
		}
		if artifact.Kind != "capture" || artifact.Action != "record" {
			return nil, errors.New("latest capture is not a record capture")
		}
		return artifact, nil
	}
	return nil, errors.New("latest capture is absent from artifact index")
}

func artifactAction(verify bool) string {
	if verify {
		return "verify"
	}
	return "mock"
}

func indexedArtifactAt(sessionDir, root string, verify bool) (*Artifact, bool, error) {
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, false, err
	}
	for _, artifact := range index.Artifacts {
		artifactRoot, err := filepath.EvalSymlinks(artifact.Path)
		if err != nil {
			return nil, false, err
		}
		if artifactRoot != root {
			continue
		}
		if verify && artifact.Provenance == "consumer" {
			return nil, false, errors.New("consumer material cannot be verified")
		}
		return &artifact, true, nil
	}
	return nil, false, nil
}
