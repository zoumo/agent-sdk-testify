package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelfSeededChildrenOwnHistoryAndReplayWithoutExternalSeed(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			python, err := exec.LookPath("python3")
			require.NoError(t, err)
			_, drivers, env := isolatedCommands(t, t.TempDir(), provider)
			agent := filepath.Join(t.TempDir(), "synthetic-self-seeding-agent")
			require.NoError(t, os.WriteFile(agent, []byte("#!"+python+"\n"+`
import json, os, pathlib, re, sys
if sys.argv[1:] == ['--version']:
    print('synthetic-self-seed-only')
    sys.exit(0)
parent = '12345678-1234-4234-8234-123456789012'
child = '87654321-4321-4321-8321-210987654321'
config = pathlib.Path(os.environ.get('CODEX_HOME', os.environ.get('CLAUDE_CONFIG_DIR')))
if 'CODEX_HOME' in os.environ:
    for identity in [parent, child]:
        path = config / 'sessions' / (identity + '.jsonl')
        path.parent.mkdir(parents=True, exist_ok=True)
        payload = {'id': identity, 'cwd': os.getcwd()}
        if identity == child: payload['parentThreadId'] = parent
        path.write_text(json.dumps({'type': 'session_meta', 'payload': payload}) + '\n')
else:
    project = config / 'projects' / re.sub(r'[^a-zA-Z0-9]', '-', os.getcwd())
    if '--resume' in sys.argv:
        assert sys.argv[sys.argv.index('--resume') + 1] == parent
        assert (project / (parent + '.jsonl')).is_file()
    else:
        for name, values in [(parent + '.jsonl', {'sessionId': parent}),
                (parent + '/subagents/agent-' + child + '.jsonl', {'sessionId': parent, 'agentId': child})]:
            path = project / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(dict(values, cwd=os.getcwd())) + '\n')
for line in sys.stdin:
    request = json.loads(line)
    print(json.dumps({'id': request['id'], 'result': {'synthetic': True}}), flush=True)
`), 0o700))
			driver := filepath.Join(drivers, "agent-sdk-testify-"+provider)
			require.NoError(t, os.WriteFile(driver, fmt.Appendf(nil, "#!%s\n", python), 0o700))
			file, err := os.OpenFile(driver, os.O_APPEND|os.O_WRONLY, 0o700)
			require.NoError(t, err)
			_, err = file.WriteString(`
import json, pathlib, subprocess, sys
request = json.loads(pathlib.Path(sys.argv[2]).read_text())
runtime = json.loads(pathlib.Path(request['runtime']).read_text())
plan = json.loads((pathlib.Path(request['materials']) / 'case.json').read_text())
assert plan['caseId'] == 'SDK-CASE-035' and 'historySeed' not in plan
for phase in ['main', 'probe']:
    peer = runtime['peer'] if phase == 'main' else runtime['peer']['probe']
    child = subprocess.run([peer['command']] + peer.get('args', []),
        input=json.dumps({'id': phase, 'method': phase}) + '\n',
        text=True, capture_output=True, timeout=15)
    assert child.returncode == 0, child.stderr
    assert json.loads(child.stdout)['result']['synthetic'] is True
report = {'provider': plan['provider'], 'caseId': plan['caseId'], 'storyId': plan['storyId'],
    'status': 'passed', 'branchStatus': 'passed', 'captureStatus': 'complete', 'gaps': [],
    'officialSDK': {'package': {'codex': 'openai-codex', 'claude': 'claude-agent-sdk'}[plan['provider']],
                    'version': 'synthetic-self-seed-only'}}
out = pathlib.Path(runtime['output'])
(out / ('replay-report.json' if runtime['replay'] else 'report.json')).write_text(json.dumps(report))
(out / 'result.json').write_text(json.dumps({'status': 'passed'}))
`)
			require.NoError(t, err)
			require.NoError(t, file.Close())
			fixture := prepareFixture(t, env, "--agent", provider, "--case", "SDK-CASE-035", "--model", "synthetic-only")
			source := readFile(t, filepath.Join(fixture, "source", "plan.json"))
			baseline := officialInventory(t, filepath.Join(fixture, "runtime", "baseline"))
			require.NoDirExists(t, filepath.Join(fixture, "source", "history-seed"))
			args := []string{"app-server", "--listen", "stdio://"}
			if provider == "claude" {
				args = []string{"--input-format", "stream-json", "--output-format", "stream-json", "--permission-prompt-tool", "stdio"}
			}
			mustRun(t, env, t.TempDir(), "", append([]string{"official", "record", "--fixture-dir", fixture, "--cmd", agent, "--"}, args...)...)
			material := latestCapture(t, fixture)
			core := fixtureMaterialRoot(t, fixture, material)
			plan := readJSON(t, filepath.Join(core, "plan.json"))
			require.NotContains(t, plan, "historySeed")
			require.Equal(t, []any{"main", "probe"}, material["connections"])
			require.Equal(t, source, readFile(t, filepath.Join(fixture, "source", "plan.json")))
			require.Equal(t, baseline, officialInventory(t, filepath.Join(fixture, "runtime", "baseline")))
			require.Equal(t, readJSON(t, filepath.Join(core, "existing-children", "setup.json")), plan["existingChildrenSetup"])
			corpus := newSyntheticCorpus(t)
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixture, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
			exported := filepath.Join(t.TempDir(), "exported")
			mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixture, "--output", exported)
			require.NoError(t, os.RemoveAll(fixture))
			require.NoError(t, os.RemoveAll(corpus.archive))
			imported := prepareFixture(t, env, "--recording", exported)
			require.NoError(t, os.RemoveAll(exported))
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", imported)
		})
	}
}
