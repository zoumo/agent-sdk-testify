package corpus

import "github.com/zoumo/agent-sdk-testify/internal/tool"

// MaterialPlanFromRuntime keeps recording facts and removes only values that
// describe one local preparation. The source plan remains a separate immutable
// binding; this projection is what replay and qualification may consume.
func MaterialPlanFromRuntime(runtime tool.Object) tool.Object {
	plan := tool.Object{}
	for key, value := range runtime {
		plan[key] = value
	}
	for _, key := range []string{
		"root", "home", "config", "raw", "tmp", "tempRoot", "output",
		"_seedRoot", "_recordedWorkspace", "_replay", "_agentResults", "_executionWorkspace", "cli",
	} {
		delete(plan, key)
	}
	return plan
}
