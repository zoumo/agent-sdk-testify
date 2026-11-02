package verify

import (
	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type (
	object           = tool.Object
	corpusValidation = corpus.Validation
)

var (
	obj       = tool.Obj
	str       = tool.Str
	array     = tool.Array
	readJSON  = tool.ReadJSON
	writeJSON = tool.WriteJSON

	fileDigest      = tool.FileDigest
	createOutput    = tool.CreateOutput
	outputBase      = tool.OutputBase
	corpusResolved  = corpus.Resolved
	corpusWithin    = corpus.Within
	corpusInventory = corpus.Inventory
	corpusPhases    = corpus.Phases
	corpusWrite     = corpus.Write
	sourceEvidence  = corpus.SourceEvidence
)

func loadMaterials(o official.Options) (object, error) {
	return catalog.Resolve(data.Scenarios(), catalog.ResolveInput{
		Mode:      o.Mode,
		Materials: o.Materials,
		Case:      o.Case,
		Provider:  o.Provider,
		Model:     o.Model,
	})
}
