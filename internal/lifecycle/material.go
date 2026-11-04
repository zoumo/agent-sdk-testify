package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type materialInspection struct {
	Plan         tool.Object
	Manifest     tool.Object
	Provenance   string
	HistoryFiles map[string]string
}

func selectionProvenance(plan, manifest tool.Object) (string, error) {
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
		// A capture introduced as an external input is not official until it
		// is published by this session's record action.
		return "external", nil
	default:
		return "", fmt.Errorf("material provenance is missing or unknown: %q", value)
	}
}

func inspectMaterial(session *Session, artifact Artifact, action string) (*materialInspection, error) {
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
		if err := checkMaterialInventory(artifact.Path, artifact); err != nil {
			return nil, err
		}
	}
	if err := corpus.VerifyMaterials(artifact.Path, true); err != nil {
		return nil, err
	}
	var plan, manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "plan.json"), &plan); err != nil {
		return nil, err
	}
	if err := tool.ReadJSON(filepath.Join(artifact.Path, "manifest.json"), &manifest); err != nil {
		return nil, err
	}
	selected, err := selectionProvenance(plan, manifest)
	if err != nil {
		return nil, err
	}
	if err := validateArtifactProvenance(session, artifact, plan, manifest, selected); err != nil {
		return nil, err
	}
	selected = artifact.Provenance
	historyFiles, err := history.Bindings(artifact.Path)
	if err != nil {
		return nil, err
	}
	provider := tool.Str(plan["provider"])
	if provider == "" {
		provider = tool.Str(plan["agentType"])
	}
	if provider != session.AgentType {
		return nil, errors.New("material agent type differs from session")
	}
	if (session.Source.Kind == "case" || session.Source.Kind == "plan") &&
		tool.Str(plan["caseId"]) != session.Source.ID {
		return nil, errors.New("material case differs from session source")
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
	if action == "verify" {
		if artifact.Provenance != "official" && artifact.Provenance != "builtin" {
			return nil, errors.New("material has no applicable official verifier: provenance must be official or builtin")
		}
		bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: provider, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
		if err != nil {
			return nil, err
		}
		if err := catalog.CheckVerifierPlan(plan, bound); err != nil {
			return nil, err
		}
	}
	return &materialInspection{Plan: plan, Manifest: manifest, Provenance: selected, HistoryFiles: historyFiles}, nil
}

func checkMaterialInventory(root string, artifact Artifact) error {
	var descriptor Artifact
	if err := tool.ReadJSON(filepath.Join(root, "material.json"), &descriptor); err != nil {
		return err
	}
	if !reflect.DeepEqual(descriptor, artifact) {
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

func validateArtifactProvenance(session *Session, artifact Artifact, plan, manifest tool.Object, selected string) error {
	claimed := artifact.Provenance
	switch claimed {
	case "official":
		if artifact.Kind == "capture" {
			if artifact.Action != "record" || session.Source.Provenance == "consumer" {
				return errors.New("official capture provenance conflicts with its action or source")
			}
			bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: session.AgentType, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
			if err != nil || tool.Str(plan["driverProfile"]) == "" || tool.Str(plan["driverProfile"]) != tool.Str(bound["driverProfile"]) {
				return errors.New("official capture has no applicable driver binding")
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
		if _, err := builtin.CheckPreparedRoot(artifact.Path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("artifact provenance is missing or unknown: %q", claimed)
	}
	return nil
}

func executionProvenance(session *Session, action string, material *Artifact) (string, error) {
	switch action {
	case "record":
		switch session.Source.Kind {
		case "case":
			return "external", nil
		case "plan":
			return "consumer", nil
		case "recording":
			if session.Source.Provenance == "consumer" {
				return "consumer", nil
			}
			return "external", nil
		default:
			return "", errors.New("record source is unknown")
		}
	case "mock", "verify":
		if material == nil {
			return "", fmt.Errorf("%s requires an input material", action)
		}
		switch material.Provenance {
		case "official", "consumer", "builtin", "external":
			return material.Provenance, nil
		default:
			return "", errors.New("input material has invalid provenance")
		}
	default:
		return "", fmt.Errorf("unknown execution action %q", action)
	}
}
