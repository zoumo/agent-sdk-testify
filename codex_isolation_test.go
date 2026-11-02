package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These peers inspect startup inputs, not Codex's runtime skill loader. They
// cross the installed runner/driver/protocol subprocess boundary without a model.
const codexIsolationNative = `import json, os, pathlib, sys
if sys.argv[1:] == ['--version']:
    print('controlled-codex 1')
    sys.exit(0)
cwd = pathlib.Path.cwd()
config = pathlib.Path(os.environ['CODEX_HOME'])
observation = {'args': sys.argv[1:], 'env': dict(os.environ), 'cwd': str(cwd),
    'config': (config / 'config.toml').read_text() if (config / 'config.toml').exists() else '',
    'auth': (config / 'auth.json').read_text() if (config / 'auth.json').exists() else '',
    'case': (cwd / 'numbers.txt').read_text(),
    'role': (config / 'agents/fixture_reader.toml').read_text() if (config / 'agents/fixture_reader.toml').exists() else '',
    'discovery': [str(p.relative_to(base)) for base in [cwd, config, pathlib.Path(os.environ['HOME'])]
        for p in base.rglob('*') if p.name in ['SKILL.md', 'AGENTS.md', 'AGENTS.override.md']]}
(cwd.parent / 'observation.json').write_text(json.dumps(observation))
for line in sys.stdin:
    message = json.loads(line)
    print(json.dumps({'id': message['id'], 'result': {}}), flush=True)
`

const codexIsolationDriver = `import json, pathlib, subprocess, sys
request = json.loads(pathlib.Path(sys.argv[2]).read_text())
peer = request['peer']
subprocess.run([peer['command'], *peer.get('args', [])], input='{"id":1,"method":"ping"}\n', text=True, check=True)
pathlib.Path(request['output'], 'result.json').write_text('{"status":"passed"}')
`

func codexIsolationCommand(t *testing.T, args []string, configured bool) (*exec.Cmd, string, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	personal := filepath.Join(root, "personal")
	require.NoError(t, writeFiles(personal, object{
		".agents/skills/private/SKILL.md": "PRIVATE_SKILL\n",
		".codex/skills/private/SKILL.md":  "PRIVATE_LEGACY_SKILL\n",
		".codex/AGENTS.md":                "PRIVATE_INSTRUCTIONS\n",
		".codex/config.toml":              "model=\"selected/model\"\nmodel_provider=\"fixture\"\ndeveloper_instructions=\"PRIVATE_INSTRUCTIONS\"\nproject_doc_fallback_filenames=[\"PRIVATE.md\"]\n[model_providers.fixture]\nname=\"fixture\"\nbase_url=\"https://example.invalid/v1\"\nwire_api=\"responses\"\nenv_key=\"ISOLATION_AUTH\"\n[model_providers.fixture.env_http_headers]\nX-Test=\"ISOLATION_HEADER\"\n[skills]\npaths=[\"/personal/skills\"]\n[agents.private]\nconfig_file=\"/personal/agent.toml\"\n",
		".codex/auth.json":                "{\"synthetic-auth\":true}",
	}))
	// Place the output under a hostile project ancestor as well as a hostile HOME.
	require.NoError(t, writeFiles(root, object{".git/HEAD": "ref: refs/heads/test\n", ".agents/skills/ancestor/SKILL.md": "ANCESTOR_SKILL", ".codex/config.toml": "developer_instructions=\"ANCESTOR\"", "AGENTS.md": "ANCESTOR_INSTRUCTIONS"}))
	materials := filepath.Join(root, "materials")
	require.NoError(t, os.Mkdir(materials, 0700))
	plan := object{"provider": "codex", "caseId": "isolation", "driverProfile": "children", "scenario": object{"files": object{"numbers.txt": "7\n11\n"}, "parameters": object{}}}
	if args != nil {
		plan["args"] = args
	}
	require.NoError(t, writeJSON(filepath.Join(materials, "plan.json"), plan))
	for name, body := range map[string]string{"native": codexIsolationNative, "driver": codexIsolationDriver} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("#!"+python+"\n"+body), 0700))
	}
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	require.NotEmpty(t, binary, "run with scripts/with-agent-sdk-testify.sh")
	output := filepath.Join(root, "attempt")
	self, err := os.Executable()
	require.NoError(t, err)
	o := options{Mode: "record", Materials: materials, Driver: filepath.Join(root, "driver"), CLI: filepath.Join(root, "native"), Output: output, Peer: binary}
	if configured {
		o.Config = filepath.Join(personal, ".codex")
	}
	optionPath := filepath.Join(root, "options.json")
	require.NoError(t, writeJSON(optionPath, o))
	cmd := exec.Command(self, "official-engine-fixture", optionPath)

	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+personal, "CODEX_HOME="+filepath.Join(personal, ".codex"), "ISOLATION_AUTH=synthetic-key", "ISOLATION_HEADER=synthetic-header", "CODEX_SKILLS_DIR=/personal/skills", "CODEX_ADDITIONAL_SKILL_ROOTS=/personal/extra")
	return cmd, output, personal
}

func TestCodexIsolationBeforeRecording(t *testing.T) {
	cmd, output, personal := codexIsolationCommand(t, nil, true)
	before, err := os.ReadFile(filepath.Join(personal, ".codex/config.toml"))
	require.NoError(t, err)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var observed struct {
		Args                          []string
		Env                           map[string]string
		Cwd, Config, Auth, Case, Role string
		Discovery                     []string
	}
	require.NoError(t, readJSON(filepath.Join(output, "observation.json"), &observed))
	assert.Contains(t, observed.Args, "project_root_markers=[]")
	assert.Contains(t, observed.Args, "project_doc_max_bytes=0")
	assert.Contains(t, observed.Args, "agents.enabled=true")
	assert.Contains(t, observed.Args, "features.multi_agent=true")
	assert.NotContains(t, observed.Args, "features.shell_tool=false")
	assert.Equal(t, filepath.Join(output, "home"), observed.Env["HOME"])
	assert.Equal(t, filepath.Join(output, "config"), observed.Env["CODEX_HOME"])
	assert.Empty(t, observed.Env["CODEX_SKILLS_DIR"])
	assert.Empty(t, observed.Env["CODEX_ADDITIONAL_SKILL_ROOTS"])
	assert.Equal(t, "synthetic-key", observed.Env["ISOLATION_AUTH"])
	assert.Equal(t, "synthetic-header", observed.Env["ISOLATION_HEADER"])
	assert.Equal(t, filepath.Join(output, "project"), observed.Cwd)
	assert.Equal(t, "7\n11\n", observed.Case)
	assert.Contains(t, observed.Role, "fixture_read")
	assert.JSONEq(t, `{"synthetic-auth":true}`, observed.Auth)
	assert.Empty(t, observed.Discovery)
	var config map[string]any
	require.NoError(t, toml.Unmarshal([]byte(observed.Config), &config))
	assert.Equal(t, "selected/model", config["model"])
	assert.Equal(t, "fixture", config["model_provider"])
	assert.NotContains(t, config, "skills")
	assert.NotContains(t, config, "developer_instructions")
	assert.NotContains(t, config, "agents")
	after, err := os.ReadFile(filepath.Join(personal, ".codex/config.toml"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	for path, want := range map[string]string{".agents/skills/private/SKILL.md": "PRIVATE_SKILL\n", ".codex/skills/private/SKILL.md": "PRIVATE_LEGACY_SKILL\n", ".codex/AGENTS.md": "PRIVATE_INSTRUCTIONS\n"} {
		got, e := os.ReadFile(filepath.Join(personal, path))
		require.NoError(t, e)
		assert.Equal(t, want, string(got))
	}
	assert.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
	assert.NoFileExists(t, filepath.Join(output, "config/auth.json"))
	var summary json.RawMessage
	require.NoError(t, readJSON(filepath.Join(output, "summary.json"), &summary))
}

func TestCodexIsolationRejectsCredentialEnvironmentOverrides(t *testing.T) {
	for _, field := range []string{"env_key", "env_http_headers"} {
		for _, name := range []string{"CODEX_HOME", "HOME", "PATH", "TMPDIR", "XDG_CONFIG_HOME", "CODEX_SKILLS_DIR", "CLAUDE_CONFIG_DIR", "NODE_OPTIONS", "PYTHONPATH", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "BASH_ENV", "AGENT_SDK_TESTIFY_BIN"} {
			t.Run(field+"/"+name, func(t *testing.T) {
				cmd, output, personal := codexIsolationCommand(t, nil, true)
				path := filepath.Join(personal, ".codex/config.toml")
				var config object
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, toml.Unmarshal(data, &config))
				provider := obj(obj(config["model_providers"])["fixture"])
				if field == "env_key" {
					provider[field] = name
				} else {
					provider[field] = object{"X-Unsafe": name}
				}
				data, err = toml.Marshal(config)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0600))
				data, err = cmd.CombinedOutput()
				require.Error(t, err, "%s", data)
				assert.Contains(t, string(data), "Codex recording isolation: reserved credential environment")
				assert.NoFileExists(t, filepath.Join(output, "observation.json"))
				assert.NoFileExists(t, filepath.Join(output, "raw/raw.jsonl"))
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
			cmd, output, _ := codexIsolationCommand(t, args, false)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			assert.Contains(t, string(data), "Codex recording isolation")
			assert.NoFileExists(t, filepath.Join(output, "observation.json"))
			assert.NoFileExists(t, filepath.Join(output, "raw/raw.jsonl"))
		})
	}
}

func TestCodexIsolationRejectsWorkspaceDiscoveryRoots(t *testing.T) {
	for _, path := range []string{".agents/skills/private/SKILL.md", ".codex/config.toml"} {
		t.Run(path, func(t *testing.T) {
			cmd, output, _ := codexIsolationCommand(t, nil, false)
			materials := filepath.Join(filepath.Dir(output), "materials/plan.json")
			var plan object
			require.NoError(t, readJSON(materials, &plan))
			obj(obj(plan["scenario"])["files"])[path] = "PRIVATE"
			require.NoError(t, writeJSON(materials, plan))
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			assert.Contains(t, string(data), "Codex recording isolation")
			assert.NoFileExists(t, filepath.Join(output, "observation.json"))
		})
	}
}

func TestCodexIsolationPreservesSuppliedCaseCapabilitiesWithoutConfig(t *testing.T) {
	args := []string{"app-server", "--stdio", "--enable", "multi_agent", "-c", `model="case/model"`,
		"-c", `sandbox_mode="read-only"`, "--config=agents.max_depth=2", "-cfeatures.shell_tool=false",
		"-c", "features.multi_agent_v2.enabled=true", "-c", "features.multi_agent_v2_dynamic_tools=true",
		"-c", "project_root_markers=[\".git\"]", "-c", "project_doc_max_bytes=99999",
		"-c", "tool_output_token_limit=4096"}
	cmd, output, _ := codexIsolationCommand(t, args, false)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var observed struct {
		Args []string
		Env  map[string]string
	}
	require.NoError(t, readJSON(filepath.Join(output, "observation.json"), &observed))
	assert.Equal(t, append(args, "-c", "project_root_markers=[]", "-c", "project_doc_max_bytes=0"), observed.Args)
	assert.Equal(t, filepath.Join(output, "home"), observed.Env["HOME"])
	assert.Empty(t, observed.Env["ISOLATION_AUTH"])
}
