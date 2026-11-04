package mock

import (
	"fmt"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func planAgentIdentity(plan tool.Object) (string, error) {
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
