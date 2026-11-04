package lifecycle

import (
	"errors"
	"fmt"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func inspectMaterial(session *Session, artifact corpus.Material) (*corpus.MaterialInspection, error) {
	inspection, err := corpus.InspectMaterial(artifact)
	if err != nil {
		return nil, err
	}
	plan := inspection.Plan
	provider := tool.Str(plan["provider"])
	if provider == "" {
		provider = tool.Str(plan["agentType"])
	}
	if provider != session.AgentType {
		return nil, errors.New("material agent type differs from session")
	}
	if (session.Source.Kind == "case" || session.Source.Kind == "plan") && tool.Str(plan["caseId"]) != session.Source.ID {
		return nil, errors.New("material case differs from session source")
	}
	if artifact.Provenance == "builtin" {
		if _, err := builtin.CheckPreparedRoot(artifact.Path); err != nil {
			return nil, err
		}
	}
	if artifact.Provenance == "official" && artifact.Kind == "capture" {
		if session.Source.Provenance == "consumer" {
			return nil, errors.New("official capture provenance conflicts with its action or source")
		}
		bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: session.AgentType, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
		if err != nil || tool.Str(plan["driverProfile"]) == "" || tool.Str(plan["driverProfile"]) != tool.Str(bound["driverProfile"]) {
			return nil, errors.New("official capture has no applicable driver binding")
		}
	}
	return inspection, nil
}

func executionProvenance(session *Session, action string, material *corpus.Material) (string, error) {
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
