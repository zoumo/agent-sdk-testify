package mock

import (
	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type (
	object           = tool.Object
	command          = tool.Command
	builtinMaterial  = builtin.Material
	builtinSelection = builtin.Selection
)

var (
	obj       = tool.Obj
	str       = tool.Str
	array     = tool.Array
	stringsOf = tool.Strings
	readJSON  = tool.ReadJSON
	writeJSON = tool.WriteJSON

	fileDigest        = tool.FileDigest
	digestBytes       = tool.DigestBytes
	copyFile          = tool.CopyFile
	copyTree          = tool.CopyTree
	writeFiles        = tool.WriteFiles
	copySaved         = tool.CopySaved
	waitDriver        = tool.WaitDriver
	materializeClaude = history.MaterializeClaude

	verifyFile         = corpus.VerifyFile
	verifySourcePhase  = corpus.VerifySourcePhase
	verifyAdoptedFiles = corpus.VerifyAdoptedFiles
	corpusRegular      = corpus.Regular
	corpusKeys         = corpus.Keys

	readBuiltin           = builtin.Read
	selectBuiltin         = builtin.Select
	extractBuiltin        = builtin.Extract
	checkBuiltinSelection = builtin.CheckSelection
	checkBuiltinMaterial  = builtin.CheckMaterial
)
