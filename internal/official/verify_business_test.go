package official

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestOfficialBusinessEvidenceRequiresExactCompleteSDKSuccess(t *testing.T) {
	for _, tc := range []struct {
		name            string
		outcome, report tool.Object
	}{
		{name: "valid"},
		{name: "outcome failed", outcome: tool.Object{"status": "failed"}},
		{name: "report failed", report: tool.Object{"status": "failed"}},
		{name: "branch failed", report: tool.Object{"branchStatus": "failed"}},
		{name: "unsupported", report: tool.Object{"status": "unsupported"}},
		{name: "untriggered", report: tool.Object{"status": "untriggered"}},
		{name: "capture incomplete", report: tool.Object{"captureStatus": "partial"}},
		{name: "wrong case", report: tool.Object{"caseId": "other"}},
		{name: "wrong story", report: tool.Object{"storyId": "other"}},
		{name: "wrong provider", report: tool.Object{"provider": "claude"}},
		{name: "wrong SDK package", report: tool.Object{"officialSDK": tool.Object{"package": "arbitrary-sdk", "version": "synthetic-only"}}},
		{name: "no SDK version", report: tool.Object{"officialSDK": tool.Object{"package": "openai-codex"}}},
		{name: "gaps", report: tool.Object{"gaps": []any{"not executed"}}},
		{name: "no business", report: tool.Object{"status": nil, "officialSDK": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, bound := tool.Object{"caseId": "case"}, tool.Object{"storyId": "story"}
			outcome := tool.Object{"status": "passed"}
			report := tool.Object{"status": "passed", "branchStatus": "passed", "captureStatus": "complete", "gaps": []any{}, "caseId": "case", "storyId": "story", "provider": "codex", "officialSDK": tool.Object{"package": "openai-codex", "version": "synthetic-test-only"}}
			maps.Copy(outcome, tc.outcome)
			maps.Copy(report, tc.report)
			err := validateBusinessEvidence("codex", plan, bound, outcome, report)
			if tc.name == "valid" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "official SDK business evidence is absent, failed or untriggered")
			}
		})
	}
}
