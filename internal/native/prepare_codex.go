package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Empty project root markers confine native .agents/.codex discovery to cwd;
// zero instruction bytes excludes AGENTS.md and configured fallback files.
// Apply after all argument preparation, including supplied recording plans.
func prepareCodexIsolation(root string, plan tool.Object) error {
	// Skill-specific case inputs need an explicit future contract; do not let
	// ordinary case files reopen the discovery roots filtered from user config.
	for _, name := range []string{".agents/skills", ".codex/skills", ".codex/config.toml"} {
		if _, err := os.Lstat(filepath.Join(tool.Str(plan["project"]), name)); err == nil {
			return fmt.Errorf("codex recording isolation: workspace contains %s", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	args := tool.Strings(plan["args"])
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var override string
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 == len(args) {
				return errors.New("codex recording isolation: missing config override")
			}
			i++
			override = args[i]
		case strings.HasPrefix(arg, "--config="):
			override = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--"):
			override = strings.TrimPrefix(arg, "-c")
		case arg == "--cd" || strings.HasPrefix(arg, "--cd=") || strings.HasPrefix(arg, "-C"),
			arg == "--profile" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "-p"),
			arg == "--code-mode-host" || strings.HasPrefix(arg, "--code-mode-host="):
			return errors.New("codex recording isolation: external workspace, profile or host")
		default:
			continue
		}
		key, _, ok := strings.Cut(override, "=")
		var config tool.Object
		if !ok {
			return errors.New("codex recording isolation: malformed config override")
		}
		if err := toml.Unmarshal([]byte(override), &config); err != nil {
			// Native CLI accepts non-TOML values as strings. Parse the key
			// separately so quoted instruction keys cannot bypass the check.
			if err := toml.Unmarshal([]byte(key+`=""`), &config); err != nil {
				return errors.New("codex recording isolation: malformed config key")
			}
		}
		if codexPersonalInputOverride(config) {
			return errors.New("codex recording isolation: personal discovery or instruction override")
		}
		if path, ok := config["model_catalog_json"]; ok {
			if path != filepath.Join(root, "config", "file-tool-model.json") {
				return errors.New("codex recording isolation: external model catalog")
			}
		}
	}
	plan["args"] = append(args, "-c", "project_root_markers=[]", "-c", "project_doc_max_bytes=0")
	return nil
}

// Reject sources of personal context, not unrelated native capability knobs.
func codexPersonalInputOverride(config tool.Object) bool {
	for key, value := range config {
		switch key {
		case "skills", "plugins", "marketplaces", "mcp_servers", "profiles",
			"instructions", "base_instructions", "developer_instructions",
			"model_instructions_file", "experimental_instructions_file":
			return true
		case "agents":
			var personalRole func(tool.Object) bool
			personalRole = func(fields tool.Object) bool {
				for name, value := range fields {
					if name == "config_file" || name == "developer_instructions" {
						return true
					}
					if nested, ok := value.(map[string]any); ok && personalRole(nested) {
						return true
					}
				}
				return false
			}
			if personalRole(tool.Obj(value)) {
				return true
			}
		}
	}
	return false
}

func codexNativeArgs(profile string, params tool.Object) []string {
	return append([]string{"app-server", "--listen", "stdio://"}, codexCaseArgs(profile, params)...)
}

func codexCaseArgs(profile string, params tool.Object) []string {
	args := []string{"-c", "features.default_mode_request_user_input=true"}
	if profile == "children" || profile == "background-children" || params["selfSeed"] == true || params["historyTarget"] == "child-messages" || params["topology"] != nil {
		args = append(args, "-c", "agents.enabled=true", "-c", "features.multi_agent=true")
	}
	if params["topology"] != nil {
		args = append(args, "-c", "agents.max_depth=2")
	}
	if params["approvalKind"] == "command" {
		args = append(args, "-c", `sandbox_mode="read-only"`)
	}
	if params["nativeFileTool"] == "apply_patch" {
		args = append(args, "-c", "features.shell_tool=false")
	}
	if params["childTools"] == "inherited-dynamic-v2" {
		args = append(args, "-c", "features.multi_agent_v2.enabled=true",
			"-c", "features.multi_agent_v2.non_code_mode_only=false",
			"-c", "features.multi_agent_v2.hide_spawn_agent_metadata=false",
			"-c", "features.multi_agent_v2_dynamic_tools=true",
			"-c", "features.shell_tool=false")
	}
	return args
}

// Unknown model slugs otherwise get fallback metadata without apply_patch.
// Declare the case's tool contract without substituting a different model.
func prepareCodexFileTool(root string, plan tool.Object) error {
	model := tool.Str(plan["model"])
	if model == "" {
		data, err := os.ReadFile(filepath.Join(root, "config", "config.toml"))
		if err != nil {
			return fmt.Errorf("file approval case needs an explicit model: %w", err)
		}
		var config struct{ Model string }
		if err := toml.Unmarshal(data, &config); err != nil {
			return err
		}
		model = config.Model
	}
	if model == "" {
		return errors.New("file approval case needs an explicit model")
	}
	plan["model"] = model
	path := filepath.Join(root, "config", "file-tool-model.json")
	catalog := tool.Object{"models": []tool.Object{{
		"slug": model, "display_name": model, "description": "Synthetic file approval case",
		"supported_reasoning_levels": []tool.Object{}, "shell_type": "disabled",
		"visibility": "hide", "supported_in_api": true, "priority": 0,
		"support_verbosity": false, "apply_patch_tool_type": "freeform",
		"base_instructions":            "Complete the synthetic file approval task using apply_patch.",
		"truncation_policy":            tool.Object{"mode": "bytes", "limit": 10000},
		"experimental_supported_tools": []string{},
	}}}
	if err := tool.WriteJSON(path, catalog); err != nil {
		return err
	}
	quoted, err := json.Marshal(path)
	if err != nil {
		return fmt.Errorf("encode model catalog path: %w", err)
	}
	plan["args"] = append(tool.Strings(plan["args"]), "-c", "model_catalog_json="+string(quoted))
	return nil
}

func prepareCodexChildFixture(root string) error {
	prompt := "Use fixture_read exactly once to read only child-fixture.txt in the inherited working directory. Return exactly SYNTHETIC_CHILD_TOKEN. Do not create agents or descendants. Do not use shell, network, search, writes, or any other file or tool."
	b, err := json.Marshal(prompt)
	if err != nil {
		return fmt.Errorf("encode child fixture prompt: %w", err)
	}
	role := "name=\"fixture_reader\"\ndescription=\"Read the single synthetic child fixture with fixture_read.\"\ndeveloper_instructions=" + string(b) + "\n"
	if err := tool.WriteFiles(filepath.Join(root, "config"), tool.Object{"agents/fixture_reader.toml": role}); err != nil {
		return err
	}
	return nil
}

// Select only variables referenced by the active native provider. A real TOML
// parser handles table and inline-table forms without a new config language.
func codexAuthEnvironment(path string, env map[string]string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var config struct {
		Provider  string `toml:"model_provider"`
		Providers map[string]struct {
			EnvKey  string            `toml:"env_key"`
			Headers map[string]string `toml:"env_http_headers"`
		} `toml:"model_providers"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("selected Codex config: %w", err)
	}
	selected := config.Providers[config.Provider]
	names := []string{selected.EnvKey}
	for _, name := range selected.Headers {
		names = append(names, name)
	}
	for _, name := range names {
		if codexReservedCredentialEnvironment(name) {
			return errors.New("codex recording isolation: reserved credential environment")
		}
		if value, ok := os.LookupEnv(name); name != "" && ok {
			env[name] = value
		}
	}
	return nil
}

// Provider header/key references are not permission to override process setup.
// Keep custom credential names, but reserve discovery and executable-loader
// namespaces even when the referenced variable is currently unset.
func codexReservedCredentialEnvironment(name string) bool {
	name = strings.ToUpper(name)
	if name == "CODEX_API_KEY" {
		return false
	}
	switch name {
	case "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA",
		"PATH", "PATHEXT", "PWD", "OLDPWD", "TMP", "TEMP", "TMPDIR",
		"SHELL", "COMSPEC", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR",
		"ENV", "BASH_ENV", "BASHOPTS", "SHELLOPTS", "ZDOTDIR", "FPATH", "CDPATH", "IFS", "PS4", "PROMPT_COMMAND",
		"LANG", "LANGUAGE", "LOCPATH", "GCONV_PATH", "TERMINFO", "TERMINFO_DIRS":
		return true
	}
	for _, prefix := range []string{"CODEX_", "CLAUDE_", "XDG_", "AGENT_SDK_", "LD_", "DYLD_", "LC_", "NODE_", "NPM_", "PYTHON", "RUBY", "GEM_", "PERL", "GIT_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Only connection and model-routing selection crosses into the isolated
// configuration. User projects, hooks, tools and instructions are not case
// materials.
func filterCodexConfig(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var source tool.Object
	if err := toml.Unmarshal(data, &source); err != nil {
		return err
	}
	selected := tool.Object{}
	for _, key := range []string{
		"model", "model_provider", "model_reasoning_effort", "openai_base_url", "model_catalog_json",
	} {
		if value, ok := source[key]; ok {
			selected[key] = value
		}
	}
	if agents := tool.Obj(source["agents"]); agents != nil {
		models := tool.Object{}
		for _, key := range []string{"default_subagent_model", "default_subagent_reasoning_effort"} {
			if value, ok := agents[key]; ok {
				models[key] = value
			}
		}
		if len(models) > 0 {
			selected["agents"] = models
		}
	}
	if memories := tool.Obj(source["memories"]); memories != nil {
		models := tool.Object{}
		for _, key := range []string{"extract_model", "consolidation_model"} {
			if value, ok := memories[key]; ok {
				models[key] = value
			}
		}
		if len(models) > 0 {
			selected["memories"] = models
		}
	}
	if desktop := tool.Obj(source["desktop"]); desktop != nil {
		if value, ok := desktop["enabled-reasoning-efforts"]; ok {
			selected["desktop"] = tool.Object{"enabled-reasoning-efforts": value}
		}
	}
	provider := tool.Str(source["model_provider"])
	if provider != "" {
		fields := tool.Obj(tool.Obj(source["model_providers"])[provider])
		connection := tool.Object{}
		for _, key := range []string{
			"name", "base_url", "env_key", "wire_api", "requires_openai_auth",
			"experimental_bearer_token", "http_headers", "env_http_headers",
			"supports_websockets", "capabilities",
		} {
			if value, ok := fields[key]; ok {
				connection[key] = value
			}
		}
		selected["model_providers"] = tool.Object{provider: connection}
	}
	filtered, err := toml.Marshal(selected)
	if err != nil {
		return err
	}
	return os.WriteFile(path, filtered, 0o600)
}
