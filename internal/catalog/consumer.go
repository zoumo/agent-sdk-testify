package catalog

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func checkConsumerAgent(agent string) error {
	switch agent {
	case "codex", "claude":
		return nil
	default:
		return fmt.Errorf("unsupported agent type %q", agent)
	}
}

func CheckConsumerPlan(plan tool.Object) error {
	version, ok := plan["version"].(float64)
	if !ok || version != 1 {
		return errors.New("consumer plan version must be 1")
	}
	agent := tool.Str(plan["agentType"])
	if err := checkConsumerAgent(agent); err != nil {
		return err
	}
	if legacy, ok := plan["provider"]; ok {
		provider, ok := legacy.(string)
		if !ok {
			return errors.New("consumer plan provider must be a string")
		}
		if provider != "" && provider != agent {
			return fmt.Errorf("consumer plan provider %q conflicts with agentType %q", provider, agent)
		}
	}
	if strings.TrimSpace(tool.Str(plan["caseId"])) == "" {
		return errors.New("consumer plan caseId is required")
	}
	if tool.Str(plan["driverProfile"]) != "consumer" {
		return errors.New("consumer plan driverProfile must be consumer")
	}
	project := tool.Str(plan["project"])
	if !filepath.IsAbs(project) {
		return errors.New("consumer plan project must be absolute")
	}
	if value, exists := plan["model"]; exists {
		if _, ok := value.(string); !ok {
			return errors.New("consumer plan model must be a string")
		}
	}
	if value, exists := plan["cliVersion"]; exists {
		if _, ok := value.(string); !ok {
			return errors.New("consumer plan cliVersion must be a string")
		}
	}
	if scenario, exists := plan["scenario"]; exists {
		scenarioObject, ok := scenario.(map[string]any)
		if !ok {
			return errors.New("consumer plan scenario must be an object")
		}
		if files, exists := scenarioObject["files"]; exists {
			_, ok := files.(map[string]any)
			if !ok {
				return errors.New("consumer plan scenario.files must be an object")
			}
		}
	}
	if _, err := PlanFiles(plan); err != nil {
		return fmt.Errorf("consumer plan %w", err)
	}
	return nil
}

func PlanFiles(plan tool.Object) (tool.Object, error) {
	files := tool.Obj(tool.Obj(plan["scenario"])["files"])
	for name, value := range files {
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("fixture %s must be text", name)
		}
		if name == "" || !filepath.IsLocal(filepath.FromSlash(name)) || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("fixture path is not local: %q", name)
		}
	}
	return files, nil
}
