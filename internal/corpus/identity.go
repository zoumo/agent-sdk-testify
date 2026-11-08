package corpus

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func materialAgentType(plan tool.Object) (string, error) {
	agentType := tool.Str(plan["agentType"])
	provider := tool.Str(plan["provider"])
	if agentType != "" && provider != "" && agentType != provider {
		return "", fmt.Errorf("material provider %q conflicts with agentType %q", provider, agentType)
	}
	if agentType != "" {
		return agentType, nil
	}
	return provider, nil
}

func manifestAgentType(manifest, rawManifest tool.Object) (string, error) {
	var found []string
	for _, value := range []tool.Object{manifest, rawManifest, tool.Obj(rawManifest["native"])} {
		agentType := tool.Str(value["agentType"])
		provider := tool.Str(value["provider"])
		if agentType != "" && provider != "" && agentType != provider {
			return "", fmt.Errorf("material manifest provider %q conflicts with agentType %q", provider, agentType)
		}
		identity := agentType
		if identity == "" {
			identity = provider
		}
		if identity != "" {
			found = append(found, identity)
		}
	}
	if len(found) > 0 {
		agent := found[0]
		for _, value := range found[1:] {
			if value != agent {
				return "", errors.New("material manifest contains conflicting agent identities")
			}
		}
		if agent != "codex" && agent != "claude" {
			return "", fmt.Errorf("unsupported agent type %q", agent)
		}
		return agent, nil
	}
	native := tool.Obj(rawManifest["native"])
	agent := ""
	for _, name := range []string{"claude", "codex"} {
		if !strings.Contains(strings.ToLower(filepath.Base(tool.Str(native["executable"]))), name) {
			continue
		}
		if agent != "" {
			return "", errors.New("material manifest does not uniquely identify its agent")
		}
		agent = name
	}
	return agent, nil
}

// RecordingAgentType reconciles every saved agent identity before fixture import.
func RecordingAgentType(plan, manifest, rawManifest tool.Object) (string, error) {
	manifestAgent, err := manifestAgentType(manifest, rawManifest)
	if err != nil {
		return "", err
	}
	planAgent, err := materialAgentType(plan)
	if err != nil {
		return "", err
	}
	if manifestAgent != "" && planAgent != "" && manifestAgent != planAgent {
		return "", fmt.Errorf("recording manifest agent %q conflicts with plan provider %q", manifestAgent, planAgent)
	}
	agent := manifestAgent
	if agent == "" {
		agent = planAgent
	}
	if agent == "" {
		return "", errors.New("recording does not identify an agent type")
	}
	if agent != "codex" && agent != "claude" {
		return "", fmt.Errorf("unsupported agent type %q", agent)
	}
	return agent, nil
}
