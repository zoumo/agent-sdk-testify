package history

import "github.com/zoumo/agent-sdk-testify/internal/tool"

type object = tool.Object

var (
	obj       = tool.Obj
	str       = tool.Str
	writeJSON = tool.WriteJSON
)

func materializeClaude(source, root, workspace string, plan object) error {
	return MaterializeClaude(source, root, workspace, plan)
}
