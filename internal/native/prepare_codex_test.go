package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
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

func TestPrepareNativeBackgroundChildGateIsScoped(t *testing.T) {
	for _, tc := range []struct {
		id, profile string
	}{
		{"SDK-CASE-019", "background-children"},
		{"SDK-CASE-016", "children"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "workspace")
			require.NoError(t, os.Mkdir(project, 0o700))
			cli := filepath.Join(root, "codex")
			require.NoError(t, os.WriteFile(cli, []byte("#!/bin/sh\nprintf 'codex-cli test\\n'\n"), 0o700))
			configPath := filepath.Join(root, "config", "config.toml")
			require.NoError(t, tool.WriteFiles(root, tool.Object{"config/config.toml": "model = \"synthetic-model\"\n"}))
			plan := tool.Object{
				"provider": "codex", "caseId": tc.id, "driverProfile": tc.profile,
				"project": project, "scenario": tool.Object{"parameters": tool.Object{"childTools": "inherited-dynamic-v2"}},
			}
			require.NoError(t, PrepareNative(context.Background(), &Options{CLI: cli}, plan, root))
			var config tool.Object
			data, err := os.ReadFile(configPath)
			require.NoError(t, err)
			require.NoError(t, toml.Unmarshal(data, &config))
			require.Equal(t, "synthetic-model", config["model"])
			role, err := os.ReadFile(filepath.Join(root, "config", "agents", "fixture_reader.toml"))
			require.NoError(t, err)
			if tc.id == "SDK-CASE-016" {
				require.NotContains(t, config, "mcp_servers")
				require.NotContains(t, string(role), "wait_for_root")
				return
			}
			gates := tool.Obj(config["mcp_servers"])
			require.Len(t, gates, 1)
			gate := tool.Obj(gates["child_gate"])
			require.Equal(t, "python3", gate["command"])
			require.Equal(t, []string{"wait_for_root"}, tool.Strings(gate["enabled_tools"]))
			require.Equal(t, []string{"deferred", "code_mode"}, tool.Strings(gate["omit_tools_from"]))
			require.Equal(t, "approve", tool.Obj(tool.Obj(gate["tools"])["wait_for_root"])["approval_mode"])
			marker := filepath.Join(root, "tmp", "child-root-completed")
			require.Equal(t, []string{"-u", "-c", codexChildGate, marker}, tool.Strings(gate["args"]))
			require.NoFileExists(t, marker)
			require.Contains(t, string(role), "mcp__child_gate__wait_for_root")
			require.NotContains(t, string(role), "SYNTHETIC_CHILD_TOKEN")
		})
	}
}
