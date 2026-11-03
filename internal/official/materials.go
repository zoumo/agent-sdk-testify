package official

import (
	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func loadMaterials(o Options) (tool.Object, error) {
	return catalog.Resolve(data.Scenarios(), catalog.ResolveInput{
		Mode:      o.Mode,
		Materials: o.Materials,
		Case:      o.Case,
		Provider:  o.Provider,
		Model:     o.Model,
	})
}
