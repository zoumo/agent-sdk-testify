package corpus

import (
	"encoding/json"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

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

// MaterialPlanBytes preserves unknown source values, including numeric literals.
// Only defined preparation facts may replace values from the source document.
func MaterialPlanBytes(source []byte, runtime tool.Object) ([]byte, error) {
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(source, &plan); err != nil {
		return nil, err
	}
	for _, key := range []string{"provider", "project", "_fixtureProject", "historyProject", "model", "initialModel", "args", "cliVersion", "cliSHA256", "stdinEOFAction", "recordingIsolation", "historySeed", "historyBindings", "sourcePlanSHA256"} {
		if value, ok := runtime[key]; ok {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			plan[key] = encoded
		}
	}
	for _, key := range []string{"root", "home", "config", "raw", "tmp", "tempRoot", "output", "_seedRoot", "_recordedWorkspace", "_replay", "_agentResults", "_executionWorkspace", "cli"} {
		delete(plan, key)
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
