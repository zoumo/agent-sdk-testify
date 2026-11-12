package lifecycle

import (
	"errors"
	"fmt"
	"slices"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func inspectMaterial(fixture *Fixture, artifact corpus.Material) (*corpus.MaterialInspection, error) {
	descriptorless, err := materialIsDescriptorless(fixture.dir, artifact.ID)
	if err != nil {
		return nil, err
	}
	return inspectMaterialWithDescriptor(fixture, artifact, !descriptorless)
}

func inspectMaterialWithDescriptor(fixture *Fixture, artifact corpus.Material, descriptor bool) (*corpus.MaterialInspection, error) {
	inspect := corpus.InspectMaterial
	if !descriptor {
		inspect = corpus.InspectMaterialWithoutDescriptor
	}
	inspection, err := inspect(artifact)
	if err != nil {
		return nil, err
	}
	plan := inspection.Plan
	provider := tool.Str(plan["provider"])
	if provider == "" {
		provider = tool.Str(plan["agentType"])
	}
	if provider != fixture.AgentType {
		return nil, errors.New("material agent type differs from fixture")
	}
	if (fixture.Source.Kind == "case" || fixture.Source.Kind == "plan") && tool.Str(plan["caseId"]) != fixture.Source.ID {
		return nil, errors.New("material case differs from fixture source")
	}
	if artifact.Provenance == "builtin" {
		if _, err := builtin.CheckPreparedRoot(artifact.Path); err != nil {
			return nil, err
		}
	}
	if artifact.Provenance == "official" && artifact.Kind == "capture" {
		if fixture.Source.Provenance == "consumer" {
			return nil, errors.New("official capture provenance conflicts with its action or source")
		}
		bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: fixture.AgentType, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
		if err != nil || tool.Str(plan["driverProfile"]) == "" || tool.Str(plan["driverProfile"]) != tool.Str(bound["driverProfile"]) {
			return nil, errors.New("official capture has no applicable driver binding")
		}
	}
	return inspection, nil
}

func materialIsDescriptorless(fixtureDir, id string) (bool, error) {
	if err := persistedSchema(MaterialIndexPath(fixtureDir), "material index"); err != nil {
		return false, err
	}
	var index MaterialIndex
	if err := tool.ReadJSON(MaterialIndexPath(fixtureDir), &index); err != nil {
		return false, err
	}
	return slices.Contains(index.Descriptorless, id), nil
}

func executionProvenance(fixture *Fixture, action string, material *corpus.Material) (string, error) {
	switch action {
	case "record":
		switch fixture.Source.Kind {
		case "case":
			return "external", nil
		case "plan":
			return "consumer", nil
		case "recording":
			if fixture.Source.Provenance == "consumer" {
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
