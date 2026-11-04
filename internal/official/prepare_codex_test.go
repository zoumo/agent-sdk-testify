package official

import (
	"context"
	"os"
	"path/filepath"
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

func TestCodexFileToolCatalogPreservesSelectedModel(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		t.Run(map[bool]string{true: "explicit", false: "configured"}[explicit], func(t *testing.T) {
			root, source := t.TempDir(), t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, "config"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(source, "config.toml"),
				[]byte("model=\"theta/glm-5.3\"\n"), 0o600))
			cli, err := os.Executable()
			require.NoError(t, err)
			plan := tool.Object{
				"provider": "codex", "driverProfile": "approval", "project": root,
				"scenario": tool.Object{"parameters": tool.Object{"approvalKind": "file", "nativeFileTool": "apply_patch"}},
			}
			if explicit {
				plan["model"] = "other/selected-model"
			}
			require.NoError(t, PrepareNative(context.Background(), &Options{CLI: cli, Config: source}, plan, root))
			catalogPath := filepath.Join(root, "config", "file-tool-model.json")
			var catalog struct {
				Models []struct {
					Slug             string `json:"slug"`
					Shell            string `json:"shell_type"`
					Patch            string `json:"apply_patch_tool_type"`
					BaseInstructions string `json:"base_instructions"`
				} `json:"models"`
			}
			require.NoError(t, tool.ReadJSON(catalogPath, &catalog))
			require.Len(t, catalog.Models, 1)
			require.Equal(t, plan["model"], catalog.Models[0].Slug)
			require.Equal(t, "disabled", catalog.Models[0].Shell)
			require.Equal(t, "freeform", catalog.Models[0].Patch)
			require.NotEmpty(t, catalog.Models[0].BaseInstructions)
			require.Contains(t, strings.Join(tool.Strings(plan["args"]), "\n"), "model_catalog_json=")
			require.Contains(t, strings.Join(tool.Strings(plan["args"]), "\n"), catalogPath)
		})
	}
	require.Error(t, prepareCodexFileTool(t.TempDir(), tool.Object{}))
}
