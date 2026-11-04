package official

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestCodexIsolationRejectsCredentialEnvironmentOverrides(t *testing.T) {
	for _, field := range []string{"env_key", "env_http_headers"} {
		for _, name := range []string{"CODEX_HOME", "HOME", "PATH", "TMPDIR", "XDG_CONFIG_HOME", "CODEX_SKILLS_DIR", "CLAUDE_CONFIG_DIR", "NODE_OPTIONS", "PYTHONPATH", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "BASH_ENV", "AGENT_SDK_TESTIFY_BIN"} {
			t.Run(field+"/"+name, func(t *testing.T) {
				provider := tool.Object{}
				if field == "env_key" {
					provider[field] = name
				} else {
					provider[field] = tool.Object{"X-Unsafe": name}
				}
				config := tool.Object{"model_provider": "fixture", "model_providers": tool.Object{"fixture": provider}}
				data, err := toml.Marshal(config)
				require.NoError(t, err)
				path := filepath.Join(t.TempDir(), "config.toml")
				require.NoError(t, os.WriteFile(path, data, 0o600))
				require.ErrorContains(t, codexAuthEnvironment(path, map[string]string{}), "codex recording isolation: reserved credential environment")
			})
		}
	}
}

func TestCodexIsolationRejectsSuppliedDiscoveryArguments(t *testing.T) {
	for _, args := range [][]string{
		{"app-server", "-c", `developer_instructions="PRIVATE"`},
		{"app-server", `--config=model_instructions_file="/personal/instructions"`},
		{"app-server", `-cskills.paths=["/personal/skills"]`},
		{"app-server", "-c", `agents.private.config_file="/personal/agent.toml"`},
		{"app-server", "-c", `model_catalog_json="/personal/catalog.json"`},
		{"app-server", "--cd", "/personal/project"},
		{"app-server", "--profile=personal"},
		{"app-server", "-c", `'developer_instructions'="PRIVATE"`},
		{"app-server", "--code-mode-host", "https://example.invalid"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			output := t.TempDir()
			plan := tool.Object{"project": output, "args": args}
			err := prepareCodexIsolation(output, plan)
			require.ErrorContains(t, err, "codex recording isolation")
			assert.NoFileExists(t, filepath.Join(output, "observation.json"))
			assert.NoFileExists(t, filepath.Join(output, "raw", "raw.jsonl"))
		})
	}
}

func TestCodexIsolationRejectsWorkspaceDiscoveryRoots(t *testing.T) {
	for _, path := range []string{".agents/skills/private/SKILL.md", ".codex/config.toml"} {
		t.Run(path, func(t *testing.T) {
			output := t.TempDir()
			project := filepath.Join(output, "project")
			require.NoError(t, tool.WriteFiles(project, tool.Object{path: "PRIVATE"}))
			err := prepareCodexIsolation(output, tool.Object{"project": project})
			require.ErrorContains(t, err, "codex recording isolation")
			assert.NoFileExists(t, filepath.Join(output, "observation.json"))
		})
	}
}

func TestClaudeRecordingRejectsExternalSettings(t *testing.T) {
	for _, args := range [][]string{
		{"--settings", "/personal/settings.json"},
		{"--settings=/personal/settings.json"},
		{"--settings", `{"env":{"HOME":"/personal"}}`},
		{`--settings={"hooks":{}}`},
		{"--settings", `{"enabledPlugins":{"personal":true}}`},
		{"--settings", `{"permissions":{"additionalDirectories":["/personal"]}}`},
		{"--settings", `{"permissions":{"ask":"Read"}}`},
		{"--settings", `null`},
		{"--settings"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			_, err := prepareClaudeArgs(tool.Object{}, args, "children")
			require.ErrorContains(t, err, "claude recording isolation")
		})
	}
}

func TestClaudeRecordingRejectsExternalInstructionAndMCPInputs(t *testing.T) {
	for _, flag := range []string{"--system-prompt-file", "--append-system-prompt-file", "--append-subagent-system-prompt-file", "--mcp-config", "--agents", "--plugin-dir"} {
		for _, args := range [][]string{{flag, "/personal/input.json"}, {flag + "=/personal/input.json"}} {
			t.Run(args[0], func(t *testing.T) {
				_, err := prepareClaudeArgs(tool.Object{}, args, "children")
				require.ErrorContains(t, err, "claude recording isolation")
			})
		}
	}
	for _, args := range [][]string{
		{"--mcp-config", `{"mcpServers":{}}`, "/personal/mcp.json"},
		{`--mcp-config={"mcpServers":{}}`, "/personal/mcp.json"},
		{"--mcp-config", "null"},
		{"--mcp-config"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			_, err := prepareClaudeArgs(tool.Object{}, args, "children")
			require.ErrorContains(t, err, "claude recording isolation")
		})
	}
}
