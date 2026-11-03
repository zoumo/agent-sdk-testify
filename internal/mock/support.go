package mock

import (
	"fmt"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type (
	object          = tool.Object
	builtinMaterial = builtin.Material
)

var (
	obj       = tool.Obj
	str       = tool.Str
	array     = tool.Array
	stringsOf = tool.Strings
	readJSON  = tool.ReadJSON
	writeJSON = tool.WriteJSON

	fileDigest  = tool.FileDigest
	digestBytes = tool.DigestBytes
	copyTree    = tool.CopyTree

	verifyFile         = corpus.VerifyFile
	verifySourcePhase  = corpus.VerifySourcePhase
	verifyAdoptedFiles = corpus.VerifyAdoptedFiles
	corpusRegular      = corpus.Regular
	corpusKeys         = corpus.Keys

	readBuiltin    = builtin.Read
	extractBuiltin = builtin.Extract
)

func planAgentType(plan object) string {
	agentType, _ := planAgentIdentity(plan)
	return agentType
}

func planAgentIdentity(plan object) (string, error) {
	agentType := str(plan["agentType"])
	provider := str(plan["provider"])
	if agentType != "" && provider != "" && agentType != provider {
		return "", fmt.Errorf("material provider %q conflicts with agentType %q", provider, agentType)
	}
	if agentType != "" {
		return agentType, nil
	}
	return provider, nil
}
