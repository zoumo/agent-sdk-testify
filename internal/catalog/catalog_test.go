package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestUnavailableVerifierReportsCapabilityAndReason(t *testing.T) {
	for _, mode := range []string{"unsupported", "planned"} {
		t.Run(mode, func(t *testing.T) {
			bound := tool.Object{"scenario": tool.Object{
				"mode": mode, "reason": "document input is unavailable on this path",
			}}
			err := CheckVerifierPlan(bound, bound)
			require.EqualError(t, err, "official case binding is "+mode+": document input is unavailable on this path")
		})
	}
}
