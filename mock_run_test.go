package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func preparedMock(t *testing.T, root string) (string, object) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "persistent")
	cmd := mockCommand(t, "--prepare", "--recording", root, "--output", output)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var descriptor object
	require.NoError(t, json.Unmarshal(data, &descriptor))
	path := filepath.Join(output, "run.json")
	var saved object
	require.NoError(t, readJSON(path, &saved))
	assert.Equal(t, saved, descriptor)
	return path, descriptor
}

func TestPreparedMockLifecycle(t *testing.T) {
	root := mockCapture(t)
	before := mockSourceBytes(t, root)
	path, run := preparedMock(t, root)
	assert.Equal(t, float64(1), run["schemaVersion"])
	workspace := str(run["workspace"])
	env := obj(run["env"])
	for _, dir := range []string{workspace, str(env["HOME"]), str(env["CODEX_HOME"]), str(env["TMPDIR"])} {
		assert.DirExists(t, dir)
	}
	peers := array(run["connections"])
	require.Len(t, peers, 1)
	peer := obj(peers[0])
	assert.Equal(t, "main", peer["id"])
	cmd := mockCommand(t, "--run", path)
	cmd.Stdin = strings.NewReader(`{"id":99,"method":"ping","params":{"unknown":9007199254740993}}` + "\n")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	assert.JSONEq(t, `{"id":99,"result":{"unknown":9007199254740993}}`, string(data))
	var receipt, process object
	require.NoError(t, readJSON(str(peer["receipt"]), &receipt))
	require.NoError(t, readJSON(str(peer["processReceipt"]), &process))
	assert.Equal(t, "passed", receipt["status"])
	assert.Equal(t, float64(0), process["exitCode"])
	assert.Equal(t, run["runId"], process["runId"])
	for i := 0; i < 2; i++ {
		data, err = mockCommand(t, "--finish", path).CombinedOutput()
		require.NoError(t, err, "%s", data)
		var finish object
		require.NoError(t, json.Unmarshal(data, &finish))
		assert.Equal(t, "passed", finish["status"])
		assert.Equal(t, true, finish["cleanupComplete"])
		assert.NoDirExists(t, workspace)
		assert.FileExists(t, str(peer["receipt"]))
		assert.FileExists(t, str(run["contract"]))
	}
	assert.Equal(t, before, mockSourceBytes(t, root))
	_, err = os.Stat(str(run["resourceRoot"]))
	assert.True(t, os.IsNotExist(err))
}

// Probe streams in existing captures have no independent plan: the parent
// manifest binds the remote phase and the case contract selects both peers.
func TestPreparedMockRequiresEveryDeclaredConnection(t *testing.T) {
	root := mockCapture(t)
	var plan, manifest object
	require.NoError(t, readJSON(filepath.Join(root, "plan.json"), &plan))
	plan["driverProfile"] = "remote-close"
	require.NoError(t, writeJSON(filepath.Join(root, "plan.json"), plan))
	require.NoError(t, copyTree(filepath.Join(root, "raw"), filepath.Join(root, "remote-probe/raw")))
	require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
	hash, err := fileDigest(filepath.Join(root, "plan.json"))
	require.NoError(t, err)
	manifest["planSHA256"] = hash
	rawHash, err := fileDigest(filepath.Join(root, "remote-probe/raw/raw.jsonl"))
	require.NoError(t, err)
	manifest["remotePhaseFiles"] = object{"remote-probe/raw/raw.jsonl": rawHash}
	require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), manifest))
	before := mockSourceBytes(t, root)
	path, run := preparedMock(t, root)
	require.Len(t, array(run["connections"]), 2)
	for _, id := range []string{"main", "probe"} {
		cmd := mockCommand(t, "--run", path, "--connection", id)
		cmd.Stdin = strings.NewReader(`{"id":9,"method":"ping","params":{"unknown":9007199254740993}}` + "\n")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	out, err := mockCommand(t, "--finish", "--run", path).CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, before, mockSourceBytes(t, root))
}

func TestPreparedMockHistoryMaterialization(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := mockCapture(t)
			var plan, manifest object
			require.NoError(t, readJSON(filepath.Join(root, "plan.json"), &plan))
			plan["provider"] = provider
			plan["project"] = "/original/project"
			plan["model"] = "synthetic-model"
			plan["scenario"] = object{"files": object{"fixture.txt": "initial fixture\n"}}
			require.NoError(t, writeJSON(filepath.Join(root, "plan.json"), plan))
			name := "config/sessions/session.jsonl"
			history := `{"cwd":"/original/project","id":"keep","future":9007199254740993,"nested":{"cwd":"keep-nested"}}` + "\n"
			if provider == "claude" {
				name = "config/projects/-original-project/session.jsonl"
			}
			require.NoError(t, writeFiles(root, object{name: history}))
			require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
			hash, err := fileDigest(filepath.Join(root, "plan.json"))
			require.NoError(t, err)
			manifest["planSHA256"] = hash
			hash, err = fileDigest(filepath.Join(root, name))
			require.NoError(t, err)
			manifest["savedFiles"] = object{name: hash}
			require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), manifest))
			before := mockSourceBytes(t, root)
			path, run := preparedMock(t, root)
			resource := str(run["resourceRoot"])
			copied, err := os.ReadFile(filepath.Join(resource, name))
			require.NoError(t, err)
			assert.Equal(t, history, string(copied))
			fixture, err := os.ReadFile(filepath.Join(str(run["workspace"]), "fixture.txt"))
			require.NoError(t, err)
			assert.Equal(t, "initial fixture\n", string(fixture))
			var materialization object
			require.NoError(t, readJSON(str(run["materialization"]), &materialization))
			if provider == "claude" {
				assert.Equal(t, run["workspace"], run["protocolCwd"])
				transforms := array(materialization["transformations"])
				require.Len(t, transforms, 1)
				transform := obj(transforms[0])
				assert.Equal(t, hash, transform["sourceSHA256"])
				derived := filepath.Join(resource, str(transform["derived"]))
				data, err := os.ReadFile(derived)
				require.NoError(t, err)
				assert.Contains(t, string(data), `"future":9007199254740993`)
				assert.Contains(t, string(data), `"nested":{"cwd":"keep-nested"}`)
				assert.Contains(t, string(data), str(run["workspace"]))
				// A derived-history drift must be reported before its temp file is removed.
				require.NoError(t, os.WriteFile(derived, []byte("corrupt\n"), 0600))
			} else {
				assert.Equal(t, "/original/project", run["protocolCwd"])
			}
			out, err := mockCommand(t, "--finish", path).CombinedOutput()
			require.Error(t, err)
			assert.NoDirExists(t, resource)
			if provider == "claude" {
				assert.Contains(t, string(out), "history")
			}
			assert.Equal(t, before, mockSourceBytes(t, root))
		})
	}
}

func TestPreparedMockBusyFinishCancellationAndCleanup(t *testing.T) {
	root := mockCapture(t)
	path, run := preparedMock(t, root)
	peer := obj(array(run["connections"])[0])
	cmd := mockCommand(t, "--run", path)
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = in.Close() })
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	_, err = in.Write([]byte(`{"id":7,"method":"ping","params":{"unknown":9007199254740993}}` + "\n"))
	require.NoError(t, err)
	_, err = bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err)
	data, err := mockCommand(t, "--finish", path).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "busy")
	var busy object
	require.NoError(t, readJSON(filepath.Join(filepath.Dir(path), "finish.json"), &busy))
	assert.Equal(t, false, busy["cleanupComplete"])
	assert.DirExists(t, str(run["resourceRoot"]))
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	require.Error(t, cmd.Wait())
	assert.Equal(t, 3, cmd.ProcessState.ExitCode())
	var receipt, process object
	require.NoError(t, readJSON(str(peer["receipt"]), &receipt))
	require.NoError(t, readJSON(str(peer["processReceipt"]), &process))
	assert.Equal(t, true, receipt["fullConsumption"])
	assert.Equal(t, false, receipt["clientEOF"])
	assert.Equal(t, float64(3), process["exitCode"])
	for i := 0; i < 2; i++ {
		data, err = mockCommand(t, "--finish", path).CombinedOutput()
		require.Error(t, err)
		assert.Contains(t, string(data), "failed")
		assert.NoDirExists(t, str(run["resourceRoot"]))
	}
}

func TestPreparedMockFailureEvidenceAndOwnership(t *testing.T) {
	for _, kind := range []string{"missing receipt", "missing process receipt", "wrong run identity", "wrong receipt identity", "strict mismatch", "early EOF", "raw drift", "plan drift", "cleanup ownership"} {
		t.Run(kind, func(t *testing.T) {
			root := mockCapture(t)
			path, run := preparedMock(t, root)
			peer := obj(array(run["connections"])[0])
			cmd := mockCommand(t, "--run", path)
			input := `{"id":99,"method":"ping","params":{"unknown":9007199254740993}}` + "\n"
			if kind == "strict mismatch" {
				input = `{"id":1,"method":"initialize"}` + "\n"
			}
			if kind == "early EOF" {
				input = ""
			}
			cmd.Stdin = strings.NewReader(input)
			data, err := cmd.CombinedOutput()
			if kind == "strict mismatch" || kind == "early EOF" {
				require.Error(t, err)
			} else {
				require.NoError(t, err, "%s", data)
			}
			switch kind {
			case "missing receipt":
				require.NoError(t, os.Remove(str(peer["receipt"])))
			case "missing process receipt":
				require.NoError(t, os.Remove(str(peer["processReceipt"])))
			case "wrong receipt identity":
				var receipt object
				require.NoError(t, readJSON(str(peer["receipt"]), &receipt))
				receipt["runId"] = "another-run"
				require.NoError(t, writeJSON(str(peer["receipt"]), receipt))
			case "wrong run identity":
				var process object
				require.NoError(t, readJSON(str(peer["processReceipt"]), &process))
				process["runId"] = "another-run"
				require.NoError(t, writeJSON(str(peer["processReceipt"]), process))
			case "raw drift":
				require.NoError(t, os.WriteFile(filepath.Join(root, "raw/raw.jsonl"), []byte("{}\n"), 0600))
			case "plan drift":
				require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), []byte("{}\n"), 0600))
			case "cleanup ownership":
				require.NoError(t, os.WriteFile(filepath.Join(str(run["resourceRoot"]), ".owner"), []byte("not-owned"), 0600))
			}
			consumer := filepath.Join(filepath.Dir(path), "consumer.txt")
			require.NoError(t, os.WriteFile(consumer, []byte("keep"), 0600))
			for i := 0; i < 2; i++ {
				data, err = mockCommand(t, "--finish", path).CombinedOutput()
				require.Error(t, err, "%s", data)
				var finish object
				require.NoError(t, readJSON(filepath.Join(filepath.Dir(path), "finish.json"), &finish))
				assert.Equal(t, "failed", finish["status"])
				if kind == "cleanup ownership" {
					assert.Equal(t, false, finish["cleanupComplete"])
					assert.DirExists(t, str(run["resourceRoot"]))
				} else {
					assert.Equal(t, true, finish["cleanupComplete"])
					assert.NoDirExists(t, str(run["resourceRoot"]))
				}
			}
			if kind == "cleanup ownership" {
				require.NoError(t, os.WriteFile(filepath.Join(str(run["resourceRoot"]), ".owner"), []byte(str(run["runId"])), 0600))
				data, err = mockCommand(t, "--finish", path).CombinedOutput()
				require.NoError(t, err, "%s", data)
				assert.NoDirExists(t, str(run["resourceRoot"]))
			}
			assert.FileExists(t, consumer)
		})
	}
}

func TestPreparedMockRejectsCorruptPrepareAndRetainsDiagnostic(t *testing.T) {
	root := mockCapture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw/raw.jsonl"), []byte("{}\n"), 0600))
	output := filepath.Join(t.TempDir(), "persistent")
	data, err := mockCommand(t, "--prepare", "--recording", root, "--output", output).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "hash mismatch")
	var diagnostic object
	require.NoError(t, readJSON(filepath.Join(output, "prepare.json"), &diagnostic))
	assert.Equal(t, "failed", diagnostic["status"])
	assert.Equal(t, true, diagnostic["cleanupComplete"])
}

func TestPreparedMockAbnormalExitCannotPass(t *testing.T) {
	root := mockCapture(t)
	path, run := preparedMock(t, root)
	cmd := mockCommand(t, "--run", path)
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = in.Close() })
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	_, err = in.Write([]byte(`{"id":7,"method":"ping","params":{"unknown":9007199254740993}}` + "\n"))
	require.NoError(t, err)
	_, err = bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	_ = in.Close()
	// The protocol child holds the run lock too. Closing its inherited input
	// lets it drain; SIGKILL cannot manufacture the missing supervisor receipt.
	require.Eventually(t, func() bool {
		data, finishErr := mockCommand(t, "--finish", path).CombinedOutput()
		if finishErr == nil {
			t.Error("killed supervisor unexpectedly passed")
			return true
		}
		return !strings.Contains(string(data), "busy")
	}, 5*time.Second, 20*time.Millisecond)
	var finish object
	require.NoError(t, readJSON(filepath.Join(filepath.Dir(path), "finish.json"), &finish))
	assert.Equal(t, "failed", finish["status"])
	assert.Equal(t, true, finish["cleanupComplete"])
	assert.NoDirExists(t, str(run["resourceRoot"]))
}

func TestPreparedMockLauncherAndIndependentAllocations(t *testing.T) {
	root := mockCapture(t)
	path, run := preparedMock(t, root)
	otherPath, other := preparedMock(t, root)
	assert.NotEqual(t, run["runId"], other["runId"])
	assert.NotEqual(t, run["workspace"], other["workspace"])
	for _, entry := range []struct {
		path       string
		descriptor object
	}{{path, run}, {otherPath, other}} {
		peer := obj(array(entry.descriptor["connections"])[0])
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, str(peer["launcher"]))
		cmd.Dir = str(entry.descriptor["workspace"])
		cmd.Env = []string{"PATH=/missing"}
		cmd.Stdin = strings.NewReader(`{"id":99,"method":"ping","params":{"unknown":9007199254740993}}` + "\n")
		data, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", data)
		data, err = mockCommand(t, "--run", entry.path).CombinedOutput()
		require.Error(t, err, "allocation must not be reused")
		data, err = mockCommand(t, "--finish", entry.path).CombinedOutput()
		require.NoError(t, err, "%s", data)
	}
}

func TestPreparedMockPartialPreparationCleansOwnedResources(t *testing.T) {
	root := mockCapture(t)
	var plan, manifest object
	require.NoError(t, readJSON(filepath.Join(root, "plan.json"), &plan))
	plan["scenario"] = object{"files": object{"../escape": "forbidden"}}
	require.NoError(t, writeJSON(filepath.Join(root, "plan.json"), plan))
	require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
	digest, err := fileDigest(filepath.Join(root, "plan.json"))
	require.NoError(t, err)
	manifest["planSHA256"] = digest
	require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), manifest))
	output := filepath.Join(t.TempDir(), "persistent")
	data, err := mockCommand(t, "--prepare", "--recording", root, "--output", output).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "escapes workspace")
	var diagnostic object
	require.NoError(t, readJSON(filepath.Join(output, "prepare.json"), &diagnostic))
	assert.Equal(t, true, diagnostic["cleanupComplete"])
	require.NotEmpty(t, diagnostic["resourceRoot"])
	assert.NoDirExists(t, str(diagnostic["resourceRoot"]))
}

func TestPreparedMockNeverCreatesTemporaryFilesInsideRecording(t *testing.T) {
	root := mockCapture(t)
	before := mockSourceBytes(t, root)
	output := filepath.Join(t.TempDir(), "persistent")
	cmd := mockCommand(t, "--prepare", "--recording", root, "--output", output)
	cmd.Env = append(cmd.Env, "TMPDIR="+root)
	data, err := cmd.CombinedOutput()
	if err == nil {
		var run object
		require.NoError(t, json.Unmarshal(data, &run))
		canonical, canonicalErr := filepath.EvalSymlinks(root)
		require.NoError(t, canonicalErr)
		assert.False(t, strings.HasPrefix(str(run["resourceRoot"]), canonical+string(filepath.Separator)), "temp root must never be inside source")
		_, _ = mockCommand(t, "--finish", filepath.Join(output, "run.json")).CombinedOutput()
	}
	assert.Equal(t, before, mockSourceBytes(t, root))
}
