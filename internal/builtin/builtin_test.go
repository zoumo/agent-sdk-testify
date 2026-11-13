package builtin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinCatalogCoverage(t *testing.T) {
	manifest, _, err := Read()
	require.NoError(t, err)
	require.Len(t, manifest.Coverage, 76)
	available := 0
	for _, row := range manifest.Coverage {
		assert.Contains(t, []string{"required", "optional"}, row["requirement"])
		if row["available"] == true {
			available++
		} else {
			assert.NotEmpty(t, row["missingReason"])
		}
		if row["caseId"] == "SDK-CASE-003" || row["caseId"] == "SDK-CASE-026" {
			assert.Equal(t, "optional", row["requirement"])
			assert.Equal(t, "unsupported", row["verification"])
			assert.Equal(t, false, row["available"])
		}
	}
	assert.Equal(t, 68, available)
}

func TestBuiltinContainsOnlyPublishedReplayCores(t *testing.T) {
	manifest, _, err := Read()
	require.NoError(t, err)
	for _, material := range manifest.Materials {
		t.Run(material.Provider+"/"+material.CaseID, func(t *testing.T) {
			require.Contains(t, material.Files, "material.json")
			require.Contains(t, material.Files, "raw/raw.jsonl")
			for name := range material.Files {
				for _, prefix := range []string{"executions/", "record/", "verify/", "material/"} {
					assert.False(t, strings.HasPrefix(name, prefix), name)
				}
				assert.NotContains(t, []string{"review.json", "adoption.json"}, name)
			}
		})
	}
}
