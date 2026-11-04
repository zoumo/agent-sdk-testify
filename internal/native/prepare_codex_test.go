package native

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestCodexCaseNativeTools(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		params        tool.Object
		want, absent  []string
	}{
		{
			"file", "approval",
			tool.Object{"approvalKind": "file", "nativeFileTool": "apply_patch"},
			[]string{"features.shell_tool=false"},
			[]string{"multi_agent_v2"},
		},
		{
			"command", "approval",
			tool.Object{"approvalKind": "command"},
			[]string{`sandbox_mode="read-only"`},
			[]string{"features.shell_tool=false", "multi_agent_v2"},
		},
		{
			"child", "children",
			tool.Object{"childTools": "inherited-dynamic-v2"},
			[]string{
				"features.multi_agent_v2.enabled=true", "features.multi_agent_v2_dynamic_tools=true",
				"features.multi_agent_v2.non_code_mode_only=false", "features.multi_agent_v2.hide_spawn_agent_metadata=false",
			},
			nil,
		},
		{"legacy child", "children", tool.Object{}, []string{"features.multi_agent=true"}, []string{"multi_agent_v2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := strings.Join(codexNativeArgs(tc.profile, tc.params), "\n")
			for _, want := range tc.want {
				require.Contains(t, args, want)
			}
			for _, absent := range tc.absent {
				require.NotContains(t, args, absent)
			}
		})
	}
}

func TestCodexFileToolRejectsMissingModel(t *testing.T) {
	require.Error(t, prepareCodexFileTool(t.TempDir(), tool.Object{}))
}
