package corpus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestOfficialExecutionSDKIdentity(t *testing.T) {
	const caseID = "SDK-CASE-001"
	rust := tool.Object{"package": "codex-app-server-remote-client", "version": "0.0.0", "revision": "108730843ae4303ae79e9b6b936343f0b7c483bb"}
	wrongRevision := tool.Object{"package": "codex-app-server-remote-client", "version": "0.0.0", "revision": "different"}
	python := tool.Object{"package": "openai-codex", "version": "0.0.0.dev0"}

	for _, tc := range []struct {
		name, provider, id string
		outcome, reported  tool.Object
		valid              bool
	}{
		{"pinned Rust", "codex", caseID, tool.Object{"sdk": rust}, rust, true},
		{"wrong Rust revision", "codex", caseID, tool.Object{"sdk": wrongRevision}, wrongRevision, false},
		{"Rust result omits identity", "codex", caseID, tool.Object{}, rust, false},
		{"Rust result and report disagree", "codex", caseID, tool.Object{"sdk": python}, rust, false},
		{"Python result and report disagree", "codex", caseID, tool.Object{"sdk": rust}, python, false},
		{"Rust unsupported case", "codex", "SDK-CASE-002", tool.Object{"sdk": rust}, rust, false},
		{"Rust wrong provider", "claude", caseID, tool.Object{"sdk": rust}, rust, false},
		{"Python", "codex", caseID, tool.Object{"sdk": python}, python, true},
		{"historical Python without result identity", "codex", caseID, tool.Object{}, python, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.valid, OfficialExecutionSDKIdentity(tc.provider, tc.id, tc.outcome, tool.Object{"officialSDK": tc.reported}))
		})
	}
	require.True(t, OfficialSDKIdentity("codex", caseID, python))
	require.False(t, OfficialSDKIdentity("codex", caseID, rust))
}
