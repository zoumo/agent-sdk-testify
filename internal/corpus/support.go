package corpus

import "github.com/zoumo/agent-sdk-testify/internal/tool"

// Package-local vocabulary for the evidence model. Implementations stay in the
// shared tool package so JSON and hash semantics cannot drift.
type (
	object = tool.Object
	result = tool.Result
)

var (
	obj       = tool.Obj
	str       = tool.Str
	array     = tool.Array
	stringsOf = tool.Strings
	readJSON  = tool.ReadJSON
	writeJSON = tool.WriteJSON
)
