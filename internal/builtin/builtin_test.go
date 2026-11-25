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
	require.Len(t, manifest.Coverage, 126)
	codexRequirements := map[string]string{"SDK-CASE-012": "required", "SDK-CASE-042": "optional"}
	availableCore := 0
	var availablePi []string
	for _, row := range manifest.Coverage {
		caseID, _ := row["caseId"].(string)
		assert.Contains(t, []string{"required", "optional"}, row["requirement"])
		if row["available"] == true {
			if row["provider"] == "pi" {
				availablePi = append(availablePi, caseID)
				assert.Equal(t, "admitted", row["adoption"])
				assert.Equal(t, "passed", row["verification"])
			} else {
				availableCore++
			}
		} else {
			assert.NotEmpty(t, row["missingReason"])
		}
		if row["caseId"] == "SDK-CASE-003" || row["caseId"] == "SDK-CASE-026" {
			assert.Equal(t, "optional", row["requirement"])
			assert.Equal(t, "unsupported", row["verification"])
			assert.Equal(t, false, row["available"])
		}
		if requirement, ok := codexRequirements[caseID]; row["provider"] == "codex" && ok {
			assert.Equal(t, requirement, row["requirement"])
			assert.Equal(t, "admitted", row["adoption"])
			assert.Equal(t, "passed", row["verification"])
			assert.Equal(t, true, row["available"])
		}
	}
	assert.Equal(t, 74, availableCore)
	assert.ElementsMatch(t, []string{
		"SDK-CASE-001", "SDK-CASE-002", "SDK-CASE-005", "SDK-CASE-013", "SDK-CASE-014",
		"SDK-CASE-021", "SDK-CASE-022", "SDK-CASE-023", "SDK-CASE-024", "SDK-CASE-030",
		"SDK-CASE-031", "SDK-CASE-033", "SDK-CASE-036", "SDK-CASE-037", "SDK-CASE-038",
	}, availablePi)
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
