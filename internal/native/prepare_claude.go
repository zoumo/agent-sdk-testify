package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func claudeNativeArgs(profile string, params tool.Object) ([]string, error) {
	var args []string
	args = []string{"--print", "--system-prompt", "Complete the supplied synthetic task in the working directory. Be concise.", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json", "--permission-prompt-tool", "stdio", "--include-partial-messages"}
	if profile != "history" && profile != "existing-children" {
		args = append(args, "--no-session-persistence")
	}
	if profile == "children" || profile == "background-children" || params["topology"] != nil {
		args = append(args, "--forward-subagent-text")
	}
	var ask []string
	switch profile {
	case "fixture-tools":
		ask = []string{"Read", "Write"}
	case "deferred-approval":
		ask = []string{"Read"}
	case "children", "background-children":
		ask = []string{"Agent", "Read"}
	}
	if params["approvalKind"] == "command" {
		ask = []string{"Bash"}
	}
	if ask != nil {
		b, err := json.Marshal(tool.Object{"permissions": tool.Object{"ask": ask}})
		if err != nil {
			return nil, fmt.Errorf("encode claude permissions: %w", err)
		}
		args = append(args, "--settings", string(b))
	}
	if params["applicationTool"] == true {
		args = append(args, "--mcp-config", `{"mcpServers":{"fixture":{"type":"sdk","name":"fixture"}}}`)
	}
	if params["configurationTiming"] == "creation" && params["setting"] == "thinking" {
		args = append(args, "--max-thinking-tokens", "1024")
	}
	if params["setting"] == "effort" {
		args = append(args, "--effort", "low")
	}
	return args, nil
}

func prepareClaudeArgs(plan tool.Object, args []string, profile string) ([]string, error) {
	// Apply at the common preparation boundary, including supplied legacy args.
	// Only the isolated, filtered user settings remain discoverable. Native
	// project/local sources also control ancestor CLAUDE.md and rules loading.
	var isolated []string
	var positional []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			positional = args[i:]
			break
		}
		if args[i] == "--setting-sources" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--setting-sources=") {
			continue
		}
		flag, value, inline := strings.Cut(args[i], "=")
		switch flag {
		case "--system-prompt", "--append-system-prompt", "--append-subagent-system-prompt":
			// Required prompt values are text, even when they look like flags.
			isolated = append(isolated, args[i])
			if !inline {
				if i+1 == len(args) {
					return nil, fmt.Errorf("claude recording isolation: missing %s value", flag)
				}
				i++
				isolated = append(isolated, args[i])
			}
			continue
		case "--system-prompt-file", "--append-system-prompt-file", "--append-subagent-system-prompt-file", "--plugin-dir":
			return nil, errors.New("claude recording isolation: external instruction or plugin input")
		case "--settings", "--mcp-config", "--agents":
			if !inline {
				if i+1 == len(args) {
					return nil, fmt.Errorf("claude recording isolation: missing %s value", flag)
				}
				i++
				value = args[i]
			}
			if flag == "--settings" && !claudeCasePermissions(value) {
				return nil, errors.New("claude recording isolation: settings must contain only inline case permissions")
			}
			if flag != "--settings" {
				var fields tool.Object
				if json.Unmarshal([]byte(value), &fields) != nil || fields == nil {
					return nil, fmt.Errorf("claude recording isolation: %s requires inline case JSON", flag)
				}
			}
			if inline && flag != "--settings" {
				isolated = append(isolated, flag+"="+value)
			} else {
				isolated = append(isolated, flag, value)
			}
			// MCP takes multiple space-separated configs, each a file or JSON.
			if flag == "--mcp-config" {
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					var fields tool.Object
					if json.Unmarshal([]byte(args[i]), &fields) != nil || fields == nil {
						return nil, errors.New("claude recording isolation: MCP requires inline case JSON")
					}
					isolated = append(isolated, args[i])
				}
			}
			continue
		}
		isolated = append(isolated, args[i])
	}
	isolated = append(isolated, "--setting-sources", "user", "--disable-slash-commands", "--strict-mcp-config")
	args = isolated
	if tool.Str(plan["model"]) != "" {
		model := tool.Str(plan["initialModel"])
		if model == "" {
			model = tool.Str(plan["model"])
		}
		args = append(args, "--model", model)
	}
	if profile == "existing-children" {
		args = append(args, "--resume", tool.Str(tool.Obj(plan["historySeed"])["sessionId"]), "--forward-subagent-text")
	}
	return append(args, positional...), nil
}

// Settings can load env, hooks and plugins even with discovery sources disabled.
// Only case approval rules and the native permission mode belong at this seam.
func claudeCasePermissions(value string) bool {
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &settings) != nil || settings == nil {
		return false
	}
	for key, raw := range settings {
		if key != "permissions" {
			return false
		}
		var permissions map[string]json.RawMessage
		if json.Unmarshal(raw, &permissions) != nil || permissions == nil {
			return false
		}
		for field, rule := range permissions {
			switch field {
			case "allow", "deny", "ask":
				var rules []string
				if json.Unmarshal(rule, &rules) != nil || rules == nil {
					return false
				}
			case "defaultMode":
				var mode string
				if json.Unmarshal(rule, &mode) != nil || mode == "" {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

func claudeAuthEnvironment(path string, env map[string]string) {
	var settings tool.Object
	if e := tool.ReadJSON(path, &settings); e == nil {
		selected := tool.Obj(settings["env"])
		// A selected endpoint must keep its own credentials; inherited
		// auth from a different endpoint is not a substitute.
		if tool.Str(selected["ANTHROPIC_BASE_URL"]) != "" {
			for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
				delete(env, k)
			}
		}
		// Settings env is untrusted discovery input too: HOME, config roots,
		// plugin paths and process injection must not escape this attempt.
		for _, key := range []string{
			"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR",
			"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_CUSTOM_HEADERS",
			"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
		} {
			if s, ok := selected[key].(string); ok {
				env[key] = s
			}
		}
	}
}

func filterClaudeConfig(path string) error {
	var source tool.Object
	if err := tool.ReadJSON(path, &source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	selected := tool.Object{}
	if model, ok := source["model"]; ok {
		selected["model"] = model
	}
	return tool.WriteJSON(path, selected)
}
