package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackageSeedClaudeMaterializationReachesNative(t *testing.T) {
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	_, _, env := isolatedCommands(t, t.TempDir(), "claude")
	root := t.TempDir()
	core := filepath.Join(root, "seed-package", "material")
	plan := object{"version": 1, "agentType": "claude", "provider": "claude", "caseId": "synthetic-seed", "driverProfile": "consumer", "project": "/old/project", "scenario": object{"files": object{}}}
	writeJSON(t, filepath.Join(core, "source-plan.json"), plan)
	sourceHash := digestFile(t, filepath.Join(core, "source-plan.json"))
	target := "config/projects/-old-project/parent.jsonl"
	saved := filepath.Join(core, "history", "runtime", filepath.FromSlash(target))
	require.NoError(t, os.MkdirAll(filepath.Dir(saved), 0o700))
	require.NoError(t, os.WriteFile(saved, []byte("{\"sessionId\":\"parent\",\"cwd\":\"/old/project\"}\n"), 0o600))
	savedHash := digestFile(t, saved)
	plan["sourcePlanSHA256"] = sourceHash
	plan["historyBindings"] = []any{object{"role": "runtime", "sessionId": "parent", "project": "/old/project", "nativeTarget": target, "source": "history/runtime/" + target, "sha256": savedHash}}
	writeJSON(t, filepath.Join(core, "plan.json"), plan)
	for _, name := range []string{"raw.jsonl", "stderr.raw"} {
		require.NoError(t, os.MkdirAll(filepath.Join(core, "raw"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(core, "raw", name), nil, 0o600))
	}
	writeJSON(t, filepath.Join(core, "raw", "manifest.json"), object{})
	writeJSON(t, filepath.Join(core, "raw", "transport.json"), object{"captureStatus": "complete", "cleanupComplete": true})
	writeJSON(t, filepath.Join(core, "report.json"), object{"status": "captured"})
	planHash := digestFile(t, filepath.Join(core, "plan.json"))
	writeJSON(t, filepath.Join(core, "manifest.json"), object{"materialFormatVersion": 3, "provenance": "capture", "status": "captured", "captureStatus": "complete", "sourcePlanSHA256": sourceHash, "planSHA256": planHash, "files": officialInventory(t, core)})
	writeJSON(t, filepath.Join(core, "material.json"), object{"formatVersion": 3, "id": "synthetic-seed", "kind": "capture", "action": "record", "provenance": "consumer", "sourcePlanSHA256": sourceHash, "planSHA256": planHash, "manifestSHA256": digestFile(t, filepath.Join(core, "manifest.json")), "recordingSHA256": digestFile(t, filepath.Join(core, "raw", "raw.jsonl")), "connections": []string{"main"}})
	consumer := filepath.Join(root, "consumer.json")
	writeJSON(t, consumer, object{"version": 1, "agentType": "claude", "provider": "claude", "caseId": "consumer", "driverProfile": "consumer", "project": "/old/project", "scenario": object{"files": object{}}})
	before := officialInventory(t, core)
	for _, shape := range []string{"core", "package"} {
		t.Run(shape, func(t *testing.T) {
			seed := core
			prefix := ""
			if shape == "package" {
				seed, prefix = filepath.Dir(core), "material/"
			}
			fixture := prepareFixture(t, env, "--plan", consumer, "--history-seed", seed)
			marker := filepath.Join(t.TempDir(), "native-started")
			native := filepath.Join(t.TempDir(), "native")
			script := fmt.Sprintf("#!%s\nimport pathlib, sys\nif sys.argv[1:] == ['--version']:\n print('controlled-seed-only'); sys.exit(0)\npathlib.Path(%q).write_text('native started')\nsys.exit(7)\n", python, marker)
			require.NoError(t, os.WriteFile(native, []byte(script), 0o700))
			result := runTool(t, env, t.TempDir(), "", "agent", "record", "--fixture-dir", fixture, "--cmd", native, "--", "--output-format", "stream-json", "--input-format", "stream-json", "--permission-prompt-tool", "stdio")
			require.Equal(t, 1, result.code, result.stderr)
			require.FileExists(t, marker, result.stderr)
			executions := array(fixtureIndex(t, fixture)["executions"])
			require.Len(t, executions, 1)
			execution := obj(executions[0])
			id := str(execution["id"])
			pkg := filepath.Join(fixture, "packages", str(execution["materialId"]))
			receipt := readJSON(t, filepath.Join(pkg, "executions", id, "materialization.json"))
			changes := array(receipt["transformations"])
			require.Len(t, changes, 1)
			change := obj(changes[0])
			require.Equal(t, "executions/"+id+"/capture/history-source/source/"+prefix+"history/runtime/"+target, change["source"])
			require.Equal(t, savedHash, change["sourceSHA256"])
			require.Equal(t, savedHash, digestFile(t, filepath.Join(pkg, str(change["source"]))))
			require.Equal(t, change["derivedSHA256"], digestFile(t, filepath.Join(pkg, str(change["derived"]))))
			require.NotEqual(t, change["sourceSHA256"], change["derivedSHA256"])
		})
	}
	require.Equal(t, before, officialInventory(t, core))
}
