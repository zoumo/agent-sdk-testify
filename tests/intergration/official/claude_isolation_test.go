package official

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// These peers observe startup configuration, not Claude's runtime skill loader.
func claudeIsolationRun(t *testing.T, settings tool.Object, args []string, rejected ...string) (tool.Object, preparationOptions, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	python, err = filepath.Abs(python)
	require.NoError(t, err)
	personal := t.TempDir()
	for _, name := range []string{".claude/skills/personal/SKILL.md", ".agents/skills/personal/SKILL.md", ".claude/CLAUDE.md", ".claude/agents/personal.md"} {
		require.NoError(t, tool.WriteFiles(personal, tool.Object{name: "PERSONAL_CONTEXT\n"}))
	}
	t.Setenv("HOME", personal)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(personal, ".claude"))
	t.Setenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD", "1")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-inherited-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "synthetic-inherited-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://synthetic-inherited.invalid")
	source := t.TempDir()
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "settings.json"), settings))
	require.NoError(t, tool.WriteJSON(filepath.Join(source, ".credentials.json"), tool.Object{"syntheticCredential": "test-only"}))
	require.NoError(t, tool.WriteFiles(source, tool.Object{"skills/source/SKILL.md": "SOURCE_PERSONAL\n", "CLAUDE.md": "SOURCE_INSTRUCTIONS\n"}))
	bin := t.TempDir()
	native := filepath.Join(bin, "native")
	require.NoError(t, os.WriteFile(native, []byte("#!"+python+"\n"+`import json, os, pathlib, sys
if sys.argv[1:] == ['--version']:
    print('controlled-claude 1')
    sys.exit(0)
config = pathlib.Path(os.environ['CLAUDE_CONFIG_DIR'])
observation = {'env': dict(os.environ), 'args': sys.argv[1:], 'cwd': os.getcwd(),
               'settings': json.loads((config / 'settings.json').read_text()) if (config / 'settings.json').exists() else {},
               'credentials': (config / '.credentials.json').exists(),
               'caseFile': pathlib.Path('numbers.txt').read_text(),
               'configFiles': sorted(str(p.relative_to(config)) for p in config.rglob('*'))}
(pathlib.Path.cwd().parent / 'native-observation.json').write_text(json.dumps(observation))
sys.stdin.readline()
print('{"type":"result","result":"controlled"}', flush=True)
sys.stdin.read()
`), 0o700))
	driver := filepath.Join(bin, "driver")
	require.NoError(t, os.WriteFile(driver, []byte("#!"+python+"\n"+`import json, pathlib, subprocess, sys
r = json.loads(pathlib.Path(sys.argv[2]).read_text())
p = r['peer']
child = subprocess.run([p['command']] + p.get('args', []), input='{"type":"user"}\n', text=True, capture_output=True, timeout=10)
sys.stderr.write(child.stderr)
pathlib.Path(r['output'], 'result.json').write_text(json.dumps({'status': 'passed' if child.returncode == 0 else 'failed'}))
sys.exit(child.returncode)
`), 0o700))
	materials := t.TempDir()
	plan := tool.Object{
		"provider": "claude", "caseId": "controlled-isolation", "driverProfile": "children",
		"scenario": tool.Object{"files": tool.Object{"numbers.txt": "7\n11\n"}},
	}
	if args != nil {
		plan["args"] = args
	}
	require.NoError(t, tool.WriteJSON(filepath.Join(materials, "plan.json"), plan))
	o := preparationOptions{Options: official.Options{Config: source, CLI: native, Model: "selected-model"}, Mode: "record", Materials: materials, Driver: driver, Output: filepath.Join(t.TempDir(), "capture")}
	if settings == nil {
		o.Config = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = runPreparationFixture(ctx, o)
	if len(rejected) != 0 {
		require.ErrorContains(t, err, rejected[0])
		assert.NoFileExists(t, filepath.Join(o.Output, "native-observation.json"))
		assert.NoFileExists(t, filepath.Join(o.Output, "raw", "raw.jsonl"))
		return nil, o, personal
	}
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(o.Output, "stderr.log"))
		t.Log(string(log))
	}
	require.NoError(t, err)
	o.Output, err = filepath.EvalSymlinks(o.Output)
	require.NoError(t, err)
	var observed tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(o.Output, "native-observation.json"), &observed))
	if settings != nil {
		var original tool.Object
		require.NoError(t, tool.ReadJSON(filepath.Join(source, "settings.json"), &original))
		assert.Equal(t, settings, original)
	}
	data, err := os.ReadFile(filepath.Join(source, "skills", "source", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "SOURCE_PERSONAL\n", string(data))
	return observed, o, personal
}

func TestClaudeRecordingPreservesInlineCaseConfiguration(t *testing.T) {
	args := []string{
		"--print", `--settings={"permissions":{"ask":["Read"],"allow":["Bash"],"deny":["Write"],"defaultMode":"default"}}`,
		`--agents={"fixture":{"description":"Synthetic","prompt":"Read numbers.txt"}}`,
		`--mcp-config={"mcpServers":{"fixture":{"type":"sdk","name":"fixture"}}}`,
		`{"mcpServers":{"other":{"type":"sdk","name":"other"}}}`,
		"--system-prompt", "--settings=/personal/prompt-text.json", "--append-system-prompt", "--plugin-dir=/personal/prompt-text",
		"--append-subagent-system-prompt", "--mcp-config=/personal/prompt-text.json", "--forward-subagent-text",
	}
	observed, _, _ := claudeIsolationRun(t, nil, args)
	got := tool.Strings(observed["args"])
	assert.Contains(t, got, `{"permissions":{"ask":["Read"],"allow":["Bash"],"deny":["Write"],"defaultMode":"default"}}`)
	for _, arg := range args[2:] {
		assert.Contains(t, got, arg)
	}
}

func TestClaudeRecordingRejectsDiscoveryEnvironmentFromSelectedSettings(t *testing.T) {
	external := t.TempDir()
	require.NoError(t, tool.WriteJSON(filepath.Join(external, "settings.json"), tool.Object{"model": "personal-model"}))
	observed, o, _ := claudeIsolationRun(t, tool.Object{
		"model": "configured-model",
		"env": tool.Object{
			"HOME": external, "CLAUDE_CONFIG_DIR": external,
			"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD": "1",
			"CLAUDE_CODE_PLUGIN_PATH":                      external, "NODE_OPTIONS": "--require personal.js",
			"ANTHROPIC_BASE_URL": "https://synthetic.invalid", "ANTHROPIC_AUTH_TOKEN": "synthetic-token",
			"ANTHROPIC_MODEL": "configured-env-model", "HTTPS_PROXY": "http://synthetic-proxy.invalid",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "configured-child-model",
		},
	}, []string{
		"--print", "--setting-sources=project,local", "--setting-sources", "user,project,local",
		"--settings", `{"permissions":{"ask":["Bash"]}}`, "--agents", `{"fixture":{"description":"Synthetic","prompt":"Read numbers.txt"}}`,
		"--mcp-config", `{"mcpServers":{"fixture":{"type":"sdk","name":"fixture"}}}`, "--forward-subagent-text",
	})
	env := tool.Obj(observed["env"])
	assert.Equal(t, filepath.Join(o.Output, "home"), env["HOME"])
	assert.Equal(t, filepath.Join(o.Output, "config"), env["CLAUDE_CONFIG_DIR"])
	for _, key := range []string{"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD", "CLAUDE_CODE_PLUGIN_PATH", "NODE_OPTIONS"} {
		assert.NotContains(t, env, key)
	}
	assert.Equal(t, "https://synthetic.invalid", env["ANTHROPIC_BASE_URL"])
	assert.Equal(t, "synthetic-token", env["ANTHROPIC_AUTH_TOKEN"])
	assert.NotContains(t, env, "ANTHROPIC_API_KEY", "a selected endpoint must not inherit credentials for another endpoint")
	assert.Equal(t, "configured-env-model", env["ANTHROPIC_MODEL"])
	assert.Equal(t, "configured-child-model", env["ANTHROPIC_DEFAULT_HAIKU_MODEL"])
	assert.Equal(t, "http://synthetic-proxy.invalid", env["HTTPS_PROXY"])
	args := tool.Strings(observed["args"])
	assert.Contains(t, args, "--disable-slash-commands")
	assert.Contains(t, args, "--strict-mcp-config")
	assert.Contains(t, args, "selected-model")
	assert.NotContains(t, args, "--setting-sources=project,local")
	assert.NotContains(t, args, "user,project,local")
	assert.Contains(t, args, `{"permissions":{"ask":["Bash"]}}`)
	assert.Contains(t, args, `{"fixture":{"description":"Synthetic","prompt":"Read numbers.txt"}}`)
	assert.Contains(t, args, `{"mcpServers":{"fixture":{"type":"sdk","name":"fixture"}}}`)
	assert.Contains(t, args, "--forward-subagent-text")
	assert.Equal(t, tool.Object{"model": "configured-model"}, tool.Obj(observed["settings"]))
}

func TestClaudeRecordingWithoutSelectedConfigIsolatesLegacyArgs(t *testing.T) {
	observed, o, _ := claudeIsolationRun(t, nil, []string{
		"--print", "--setting-sources", "project,local",
		"--permission-prompt-tool", "stdio", "--forward-subagent-text", "--", "synthetic prompt",
	})
	args := tool.Strings(observed["args"])
	assert.Contains(t, args, "--disable-slash-commands")
	assert.Less(t, slices.Index(args, "--disable-slash-commands"), slices.Index(args, "--"), "isolation must be parsed as options, not prompt text")
	assert.Contains(t, args, "--strict-mcp-config")
	require.Contains(t, args, "--setting-sources")
	for i, arg := range args {
		if arg == "--setting-sources" {
			require.Less(t, i+1, len(args))
			assert.Equal(t, "user", args[i+1])
		}
	}
	env := tool.Obj(observed["env"])
	assert.Equal(t, filepath.Join(o.Output, "home"), env["HOME"])
	assert.Equal(t, filepath.Join(o.Output, "config"), env["CLAUDE_CONFIG_DIR"])
	assert.Equal(t, "synthetic-inherited-key", env["ANTHROPIC_API_KEY"])
	assert.Empty(t, observed["configFiles"])
	assert.Contains(t, args, "--forward-subagent-text")
	assert.Contains(t, args, "--permission-prompt-tool")
	assert.Contains(t, args, "selected-model")
}

func TestClaudeRecordingIsolatesDiscoveryBeforeStartup(t *testing.T) {
	observed, o, personal := claudeIsolationRun(t, tool.Object{
		"model": "configured-model", "hooks": tool.Object{"SessionStart": []any{}},
		"enabledPlugins": tool.Object{"personal": true},
	}, nil)
	args := tool.Strings(observed["args"])
	assert.Contains(t, args, "--disable-slash-commands")
	require.Contains(t, args, "--setting-sources")
	for i, arg := range args {
		if arg == "--setting-sources" {
			require.Less(t, i+1, len(args))
			assert.Equal(t, "user", args[i+1])
		}
	}
	assert.Contains(t, args, "--strict-mcp-config")
	assert.Contains(t, args, "--forward-subagent-text")
	assert.Contains(t, args, "--permission-prompt-tool")
	assert.Contains(t, args, `{"permissions":{"ask":["Agent","Read"]}}`)
	assert.NotContains(t, args, "--bare")
	assert.NotContains(t, args, "--safe-mode")
	assert.Equal(t, tool.Object{"model": "configured-model"}, tool.Obj(observed["settings"]))
	assert.Equal(t, true, observed["credentials"])
	assert.Equal(t, "7\n11\n", observed["caseFile"])
	assert.Equal(t, filepath.Join(o.Output, "project"), observed["cwd"])
	env := tool.Obj(observed["env"])
	assert.Equal(t, filepath.Join(o.Output, "home"), env["HOME"])
	assert.Equal(t, filepath.Join(o.Output, "config"), env["CLAUDE_CONFIG_DIR"])
	assert.NotContains(t, env, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD")
	assert.Equal(t, []string{".credentials.json", "settings.json"}, tool.Strings(observed["configFiles"]))
	for _, name := range []string{".claude/skills/personal/SKILL.md", ".agents/skills/personal/SKILL.md", ".claude/CLAUDE.md", ".claude/agents/personal.md"} {
		data, err := os.ReadFile(filepath.Join(personal, name))
		require.NoError(t, err)
		assert.Equal(t, "PERSONAL_CONTEXT\n", string(data))
	}
}

func TestClaudePreparationRejectsExternalSettingsBeforeCapture(t *testing.T) {
	claudeIsolationRun(t, nil, []string{"--settings", "/personal/settings.json"}, "claude recording isolation")
}
