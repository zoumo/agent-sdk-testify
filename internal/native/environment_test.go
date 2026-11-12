package native

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestCopyNativeConfigKeepsCodexMagpieRoutingAndCatalog(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source", ".codex")
	require.NoError(t, os.MkdirAll(source, 0o700))
	catalog := []byte(`{"models":[{"slug":"synthetic/model"}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(source, "magpie-desktop-models.json"), catalog, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "auth.json"), []byte(`{"syntheticToken":true}`), 0o600))
	config := `model = "synthetic/model"
model_provider = "magpie"
model_reasoning_effort = "max"
openai_base_url = "http://127.0.0.1:3425/backend-api/codex"
model_catalog_json = "magpie-desktop-models.json"

[model_providers.magpie]
name = "magpie"
base_url = "http://127.0.0.1:3425/v1"
wire_api = "responses"
experimental_bearer_token = "magpie"
requires_openai_auth = true
http_headers = { x-openai-actor-authorization = "magpie" }
capabilities = { synthetic = true }

[agents]
default_subagent_model = "synthetic/subagent"
default_subagent_reasoning_effort = "low"
max_depth = 3

[memories]
extract_model = "synthetic/memory-extract"
consolidation_model = "synthetic/memory-consolidate"
generate_memories = true

[desktop]
enabled-reasoning-efforts = ["low", "medium", "max"]
localeOverride = "zh-CN"

[mcp_servers.private]
command = "must-not-run"

[projects.private]
trust_level = "trusted"
`
	require.NoError(t, os.WriteFile(filepath.Join(source, "config.toml"), []byte(config), 0o600))
	t.Setenv("CODEX_HOME", source)

	destination := filepath.Join(root, "session", "config")
	private := filepath.Join(root, "session", "private-config")
	env := map[string]string{}
	require.NoError(t, CopyNativeConfig("codex", destination, destination, private, env))

	var copied tool.Object
	data, err := os.ReadFile(filepath.Join(destination, "config.toml"))
	require.NoError(t, err)
	require.NoError(t, toml.Unmarshal(data, &copied))
	require.Equal(t, "synthetic/model", copied["model"])
	require.Equal(t, "magpie", copied["model_provider"])
	require.Equal(t, "max", copied["model_reasoning_effort"])
	require.Equal(t, "http://127.0.0.1:3425/backend-api/codex", copied["openai_base_url"])
	require.Equal(t, filepath.Join(destination, "model-catalog.json"), copied["model_catalog_json"])
	provider := tool.Obj(tool.Obj(copied["model_providers"])["magpie"])
	require.Equal(t, "magpie", provider["experimental_bearer_token"])
	require.Equal(t, "magpie", tool.Obj(provider["http_headers"])["x-openai-actor-authorization"])
	require.Equal(t, true, tool.Obj(provider["capabilities"])["synthetic"])
	require.Equal(t, "synthetic/subagent", tool.Obj(copied["agents"])["default_subagent_model"])
	require.Equal(t, "low", tool.Obj(copied["agents"])["default_subagent_reasoning_effort"])
	require.NotContains(t, tool.Obj(copied["agents"]), "max_depth")
	require.Equal(t, "synthetic/memory-extract", tool.Obj(copied["memories"])["extract_model"])
	require.Equal(t, "synthetic/memory-consolidate", tool.Obj(copied["memories"])["consolidation_model"])
	require.NotContains(t, tool.Obj(copied["memories"]), "generate_memories")
	require.Equal(t, []any{"low", "medium", "max"}, tool.Obj(copied["desktop"])["enabled-reasoning-efforts"])
	require.NotContains(t, tool.Obj(copied["desktop"]), "localeOverride")
	require.NotContains(t, copied, "mcp_servers")
	require.NotContains(t, copied, "projects")
	require.Equal(t, catalog, mustReadFile(t, filepath.Join(destination, "model-catalog.json")))
	require.FileExists(t, filepath.Join(private, "auth.json"))
	require.Equal(t, config, string(mustReadFile(t, filepath.Join(source, "config.toml"))))
}

func TestCopyNativeConfigRejectsMissingCodexCatalog(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source", ".codex")
	require.NoError(t, os.MkdirAll(source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "config.toml"), []byte(`model_catalog_json = "missing.json"`), 0o600))
	t.Setenv("CODEX_HOME", source)

	err := CopyNativeConfig("codex", filepath.Join(root, "session", "config"), filepath.Join(root, "session", "config"), filepath.Join(root, "session", "private-config"), map[string]string{})
	require.ErrorContains(t, err, "codex model catalog")
}

func TestCopyNativeConfigKeepsClaudeRoutingSettings(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source", ".claude")
	require.NoError(t, os.MkdirAll(source, 0o700))
	settings := tool.Object{
		"model":             "theta/DeepSeek-V4.1-Flash-1017[1m]",
		"effortLevel":       "medium",
		"autoCompactWindow": 240000,
		"modelSettings": tool.Object{
			"claude-opus-5-5": tool.Object{"effortLevel": "xhigh"},
		},
		"ultracode": true,
		"env": tool.Object{
			"ANTHROPIC_BASE_URL":                                  "http://127.0.0.1:3425",
			"ANTHROPIC_AUTH_TOKEN":                                "synthetic-token",
			"ANTHROPIC_DEFAULT_FABLE_MODEL":                       "theta/GLM-5.3[1M][1m]",
			"ANTHROPIC_SMALL_FAST_MODEL":                          "theta/GLM-5.3[1m]",
			"CLAUDE_CODE_SUBAGENT_MODEL":                          "theta/GLM-5.3[1m]",
			"CLAUDE_CODE_AUTO_MODE_MODEL":                         "theta/GLM-5.3[1m]",
			"CLAUDE_CODE_EFFORT_LEVEL":                            "max",
			"CLAUDE_CODE_MAX_CONTEXT_TOKENS":                      "1000000",
			"CLAUDE_CODE_AUTO_COMPACT_WINDOW":                     "900000",
			"CLAUDE_CODE_MAX_OUTPUT_TOKENS":                       "32000",
			"CLAUDE_CODE_MODEL_CAPABILITIES":                      "synthetic/model=effort,max_effort",
			"CLAUDE_CODE_AUTO_MODE_SERVER":                        "0",
			"CLAUDE_CODE_DISABLE_EXPLORE_INHERIT_CAP":             "1",
			"ENABLE_TOOL_SEARCH":                                  "true",
			"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":                   "Opus via magpie",
			"ANTHROPIC_DEFAULT_OPUS_MODEL_DESCRIPTION":            "synthetic description",
			"ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES": "effort,xhigh_effort",
			"HOME":                    "/personal",
			"CLAUDE_CODE_PLUGIN_PATH": "/personal/plugins",
		},
		"hooks":          tool.Object{"private": true},
		"enabledPlugins": tool.Object{"private": true},
		"modelPicker":    tool.Object{"options": []any{}},
	}
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "settings.json"), settings))
	require.NoError(t, tool.WriteJSON(filepath.Join(source, ".credentials.json"), tool.Object{"syntheticToken": true}))
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	destination := filepath.Join(root, "session", "config")
	private := filepath.Join(root, "session", "private-config")
	env := map[string]string{}
	require.NoError(t, CopyNativeConfig("claude", destination, destination, private, env))

	var copied tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(destination, "settings.json"), &copied))
	require.Equal(t, "theta/DeepSeek-V4.1-Flash-1017[1m]", copied["model"])
	require.Equal(t, "medium", copied["effortLevel"])
	require.Equal(t, true, copied["ultracode"])
	require.InDelta(t, 240000, copied["autoCompactWindow"], 0)
	require.Equal(t, tool.Obj(settings["modelSettings"]), copied["modelSettings"])
	require.NotContains(t, copied, "env")
	require.NotContains(t, copied, "hooks")
	require.NotContains(t, copied, "enabledPlugins")
	require.NotContains(t, copied, "modelPicker")

	for key, want := range tool.Obj(settings["env"]) {
		if key == "HOME" || key == "CLAUDE_CODE_PLUGIN_PATH" {
			require.NotContains(t, env, key)
			continue
		}
		require.Equal(t, want, env[key])
	}
	require.FileExists(t, filepath.Join(private, ".credentials.json"))
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
