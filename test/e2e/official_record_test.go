package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// This driver deliberately uses only installed process contracts. It models the
// SDK launching the supplied executable, including SDKs that drop inherited FDs.
func controlledOfficialDriver(args []string) int {
	if len(args) != 4 || args[2] != "--request" {
		return 2
	}
	mode := args[0]
	var request conformanceDriverRequest
	if readJSONFile(args[3], &request) != nil {
		return 2
	}
	var runtime conformanceRuntime
	if readJSONFile(request.Runtime, &runtime) != nil || runtime.SchemaVersion != 1 {
		return 2
	}
	fixtureDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(runtime.Output))))
	var plan object
	if readJSONFile(filepath.Join(request.Materials, "case.json"), &plan) != nil {
		return 2
	}
	if mode == "crash" {
		return 9
	}
	if mode == "hold-before" {
		driverCheckpoint(runtime.Output, mode)
		waitDriverCancellation()
		return 0
	}
	if mode == "cancel-before" {
		waitDriverCancellation()
		return 0
	}
	report := object{"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"], "status": "passed", "branchStatus": "passed", "gaps": []any{}, "captureStatus": "complete", "officialSDK": object{"package": "openai-codex", "version": "synthetic-official-driver"}}
	if mode == "busy" && runtime.Replay {
		report["verifyBefore"] = driverAction(runtime.Output, args[1], "official", "verify")
		report["recordBefore"] = driverAction(runtime.Output, args[1], "agent", "record", "--cmd", "/usr/bin/false", "--", "app-server", "--listen", "stdio://")
		report["officialRecordBefore"] = driverAction(runtime.Output, args[1], "official", "record", "--cmd", "/usr/bin/false", "--", "app-server", "--listen", "stdio://")
	}
	report["host"] = object{"home": os.Getenv("HOME"), "config": os.Getenv("CODEX_HOME"), "workspace": runtime.Workspace}

	if mode != "no-trigger" && mode != "reuse-ref" {
		peers := []conformanceCommand{runtime.Peer.conformanceCommand}
		if runtime.Peer.Probe != nil {
			peers = append(peers, *runtime.Peer.Probe)
		}
		for i, peer := range peers {
			cmd := exec.CommandContext(context.Background(), peer.Command, peer.Args...)
			cmd.Dir = runtime.Workspace
			cmd.Stderr = os.Stderr
			in, err := cmd.StdinPipe()
			if err != nil {
				return 3
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				return 3
			}
			if cmd.Start() != nil {
				return 3
			}
			if mode == "early-exit" {
				_ = in.Close()
				_ = cmd.Wait()
				return 5
			}
			// cwd is part of the native payload, not just a process directory.
			payload, marshalErr := json.Marshal(object{"id": 1, "method": "conformance", "params": object{"cwd": plan["project"]}})
			if marshalErr != nil {
				return 4
			}
			if _, err := fmt.Fprintln(in, string(payload)); err != nil {
				return 4
			}
			if _, err := bufio.NewReader(out).ReadBytes('\n'); err != nil {
				return 4
			}
			if mode == "cancel-during" || mode == "hold-during" {
				driverCheckpoint(runtime.Output, mode)
				waitDriverCancellation()
				_ = in.Close()
				_ = cmd.Wait()
				return 0
			}
			_ = in.Close()
			if cmd.Wait() != nil {
				return 5
			}
			if i == 0 && len(peers) > 1 {
				var finished object
				if readJSONFile(filepath.Join(fixtureDir, "current.json"), &finished) != nil {
					return 5
				}
				report["currentAfterMain"] = finished["materialId"]
				notices, err := os.ReadFile(filepath.Join(runtime.Output, "peer-main.results.jsonl"))
				if err != nil {
					return 5
				}
				lines := strings.Split(strings.TrimSpace(string(notices)), "\n")
				if len(lines) != 2 || json.Unmarshal([]byte(lines[1]), &finished) != nil {
					return 5
				}
				report["mainResultHasMaterial"] = finished["material"] != nil
			}
		}
	}

	if mode == "lost-notice" || mode == "partial-notice" {
		path := filepath.Join(runtime.Output, "peer-main.results.jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			return 6
		}
		if mode == "lost-notice" {
			data = nil
		} else {
			data = data[:len(data)-1]
		}
		if os.WriteFile(path, data, 0o600) != nil {
			return 6
		}
	}
	if mode == "hold-after" {
		driverCheckpoint(runtime.Output, mode)
		waitDriverCancellation()
	}
	if mode == "local" {
		data, err := os.ReadFile(filepath.Join(runtime.Output, "peer-main.results.jsonl"))
		var finished, execution object
		rows := strings.Split(strings.TrimSpace(string(data)), "\n")
		if err != nil || len(rows) != 2 || json.Unmarshal([]byte(rows[1]), &finished) != nil || readJSONFile(filepath.Join(fixtureDir, "packages", str(finished["materialId"]), filepath.FromSlash(str(finished["execution"]))), &execution) != nil {
			return 6
		}
		host, hostErr := os.Stat(os.Getenv("CODEX_HOME"))
		overlay, overlayErr := os.Stat(filepath.Join(fixtureDir, "runtime", "executions", str(execution["executionId"]), "config"))
		if hostErr != nil || overlayErr != nil || !os.SameFile(host, overlay) {
			return 6
		}
		report["hostRetainedExecution"] = true
	}
	if mode == "bad-result" {
		_ = os.WriteFile(filepath.Join(runtime.Output, "result.json"), []byte("{"), 0o600)
		return 0
	}
	if mode == "wrong-hash" || mode == "replay-schema-missing" || mode == "process-passed-error" {
		data, err := os.ReadFile(filepath.Join(runtime.Output, "peer-main.results.jsonl"))
		if err != nil {
			return 6
		}
		rows := strings.Split(strings.TrimSpace(string(data)), "\n")
		var finished object
		if len(rows) != 2 || json.Unmarshal([]byte(rows[1]), &finished) != nil {
			return 6
		}
		path := str(finished["receipt"])
		if mode == "process-passed-error" {
			path = str(finished["processReceipt"])
		}
		path = filepath.Join(fixtureDir, "packages", str(finished["materialId"]), filepath.FromSlash(path))
		var receipt object
		if readJSONFile(path, &receipt) != nil {
			return 6
		}
		switch mode {
		case "wrong-hash":
			receipt["recordingSHA256"] = "wrong"
		case "replay-schema-missing":
			delete(receipt, "schemaVersion")
		case "process-passed-error":
			receipt["error"] = "synthetic process receipt failure"
		}
		if writeJSONFile(path, receipt) != nil {
			return 6
		}
	}
	if mode == "process-not-started" {
		path := filepath.Join(runtime.Output, "peer-main.process.json")
		var process object
		if readJSONFile(path, &process) != nil {
			return 6
		}
		process["started"] = false
		if writeJSONFile(path, process) != nil {
			return 6
		}
	}

	if mode == "reuse-ref" {
		var paths map[string]string
		if readJSONFile(filepath.Join(fixtureDir, "reuse-reference.json"), &paths) != nil {
			return 6
		}
		for name, source := range paths {
			data, err := os.ReadFile(source)
			if err != nil || os.WriteFile(filepath.Join(runtime.Output, name), data, 0o600) != nil {
				return 6
			}
		}
	}
	if mode == "business-failed" {
		report["status"], report["branchStatus"] = "failed", "failed"
	}
	if mode == "incomplete-capture" {
		report["captureStatus"] = "partial"
	}
	if mode == "wrong-ref" {
		path := filepath.Join(runtime.Output, "peer-main.results.jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			return 6
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var value object
		if len(lines) != 2 || json.Unmarshal([]byte(lines[1]), &value) != nil {
			return 6
		}
		value["executionId"] = "unrelated-execution"
		encoded, err := json.Marshal(value)
		if err != nil {
			return 6
		}
		if os.WriteFile(path, []byte(lines[0]+"\n"+string(encoded)+"\n"), 0o600) != nil {
			return 6
		}
	}
	if mode == "credential-leak" {
		report["selectedCredential"] = os.Getenv("OPENAI_API_KEY")
	}
	reportName := "report.json"
	if runtime.Replay {
		reportName = "replay-report.json"
	}
	if writeJSONFile(filepath.Join(runtime.Output, reportName), report) != nil {
		return 7
	}
	if writeJSONFile(filepath.Join(runtime.Output, "result.json"), object{"status": report["status"], "sdk": report["officialSDK"]}) != nil {
		return 7
	}
	return 0
}

func driverAction(output, binary string, args ...string) int {
	fixtureDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(output))))
	// Fixture flags precede native argv, exactly as the installed CLI requires.
	offset := len(args)
	for i, arg := range args {
		if arg == "--" {
			offset = i
			break
		}
	}
	argv := append([]string{}, args[:offset]...)
	argv = append(argv, "--fixture-dir", fixtureDir)
	argv = append(argv, args[offset:]...)
	cmd := exec.CommandContext(context.Background(), binary, argv...)
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func officialRunResult(t *testing.T, fixtureDir string) object {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(fixtureDir, "packages", "*", "record", "*", "result.json"))
	require.NoError(t, err)
	failures, err := filepath.Glob(filepath.Join(fixtureDir, "failures", "official-record", "*", "result.json"))
	require.NoError(t, err)
	paths = append(paths, failures...)
	require.Len(t, paths, 1)
	return readJSON(t, paths[0])
}

func TestOfficialRecordInstalledChain(t *testing.T) {
	for _, mode := range []string{
		"local", "busy", "business-failed", "incomplete-capture",
		"no-trigger", "wrong-ref", "process-not-started",
		"cancel-before", "cancel-during", "startup-failed",
	} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			agent, drivers, env := isolatedCommands(t, root, "codex")
			// The driver is explicitly synthetic. No official SDK is installed.
			writeExecutable(t, filepath.Join(drivers, "agent-sdk-testify-codex"), os.Executable, "conformance-official-driver", mode, os.Getenv("AGENT_SDK_TESTIFY_BIN"))
			if mode == "startup-failed" {
				require.NoError(t, os.WriteFile(filepath.Join(drivers, "agent-sdk-testify-codex"), []byte("#!/missing/interpreter\n"), 0o700))
			}
			fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			source := obj(readJSON(t, filepath.Join(fixtureDir, "fixture.json"))["source"])
			sourcePath := resolveFixtureReference(t, fixtureDir, str(source["path"]))
			sourceBytes := readFile(t, sourcePath)
			previous := currentMaterialID(t, fixtureDir)
			args := []string{"official", "record", "--fixture-dir", fixtureDir}
			if mode == "cancel-before" {
				args = append(args, "--timeout", "1s")
			}
			args = append(args, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			var command commandResult
			if mode == "cancel-during" {
				command = cancelOfficialDuringProtocol(t, env, fixtureDir, args...)
			} else {
				command = runTool(t, env, t.TempDir(), "", args...)
			}
			if mode == "wrong-ref" {
				require.Equal(t, 1, command.code, command.stderr)
				journal := readJSON(t, filepath.Join(fixtureDir, "official.json"))
				require.NotEmpty(t, journal["materialId"])
				require.NotEmpty(t, journal["executionId"])
				require.DirExists(t, filepath.Join(fixtureDir, filepath.FromSlash(str(journal["staging"]))))
				require.NoDirExists(t, filepath.Join(fixtureDir, filepath.FromSlash(str(journal["target"]))))
				require.Equal(t, journal["materialId"], currentMaterialID(t, fixtureDir))
				require.Equal(t, sourceBytes, readFile(t, sourcePath))
				return
			}
			result := officialRunResult(t, fixtureDir)
			if mode == "local" || mode == "busy" {
				require.Zero(t, command.code, command.stderr)
				require.Equal(t, "passed", result["status"])
			} else {
				require.Equal(t, 1, command.code, command.stderr)
				require.Equal(t, "failed", result["status"])
			}
			if mode == "process-not-started" {
				require.Contains(t, command.stderr, "protocol process failed or was not reaped")
			}
			require.Equal(t, sourceBytes, readFile(t, sourcePath))
			require.NoFileExists(t, filepath.Join(fixtureDir, "official.json"))
			if mode == "no-trigger" || mode == "cancel-before" || mode == "startup-failed" {
				require.Empty(t, array(fixtureIndex(t, fixtureDir)["executions"]))
				require.Equal(t, previous, currentMaterialID(t, fixtureDir))
				return
			}
			require.Len(t, array(fixtureIndex(t, fixtureDir)["executions"]), 1)
			if mode == "cancel-during" {
				require.Equal(t, previous, currentMaterialID(t, fixtureDir))
				return
			}
			material := latestCapture(t, fixtureDir)
			require.NotEqual(t, previous, material["id"])
			require.Equal(t, "official", material["provenance"])
			materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
			require.NotEmpty(t, readJSON(t, filepath.Join(materialRoot, "official-origin.json"))["runId"])
			require.Equal(t, sourceBytes, readFile(t, filepath.Join(materialRoot, "source-plan.json")))
			assertMaterialHashes(t, materialRoot, material)
			require.Equal(t, "captured", readJSON(t, filepath.Join(materialRoot, "report.json"))["status"])
			require.NoFileExists(t, filepath.Join(materialRoot, "result.json"))
			if mode == "local" {
				recordRoot := filepath.Join(fixtureDir, "packages", str(material["id"]), "record", str(result["id"]))
				require.Equal(t, true, readJSON(t, filepath.Join(recordRoot, "report.json"))["hostRetainedExecution"])
				plan := readJSON(t, filepath.Join(materialRoot, "plan.json"))
				payload, marshalErr := json.Marshal(object{"id": 1, "method": "conformance", "params": object{"cwd": plan["project"]}})
				require.NoError(t, marshalErr)
				replayed := runPreparedMock(t, env, fixtureDir, "main", string(payload)+"\n")
				require.Contains(t, replayed.stdout, `"synthetic":true`)
				exported := filepath.Join(t.TempDir(), "exported")
				mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", exported)
				originalProof := readFile(t, filepath.Join(exported, "official-origin.json"))
				// Removing the original fixture makes hidden old-fixture lookups fail.
				require.NoError(t, os.RemoveAll(fixtureDir))
				imported := prepareFixture(t, env, "--recording", exported)
				importedMaterial := latestCapture(t, imported)
				require.Equal(t, originalProof, readFile(t, filepath.Join(fixtureMaterialRoot(t, imported, importedMaterial), "official-origin.json")))
				require.Equal(t, "official", importedMaterial["provenance"])
				replayed = runPreparedMock(t, env, imported, "main", string(payload)+"\n")
				require.Contains(t, replayed.stdout, `"synthetic":true`)
			}
		})
	}
}

func TestOfficialRecordExistingRemote(t *testing.T) {
	for _, target := range []string{"single", "multi", "owned"} {
		caseID := "SDK-CASE-001"
		if target != "single" {
			caseID = "SDK-CASE-032"
		}
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			agent, drivers, env := isolatedCommands(t, root, "codex")
			writeExecutable(t, filepath.Join(drivers, "agent-sdk-testify-codex"), os.Executable, "conformance-official-driver", "remote", os.Getenv("AGENT_SDK_TESTIFY_BIN"))
			fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", caseID, "--model", "synthetic-model")
			previous := currentMaterialID(t, fixtureDir)
			remote := newControlledRemote(t)
			args := append([]string{"official", "record", "--fixture-dir", fixtureDir}, remote.args("/success")...)
			if target == "owned" {
				args = []string{"official", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://"}
			}
			command := runTool(t, env, t.TempDir(), "", args...)
			require.Zero(t, command.code, command.stderr)
			result := officialRunResult(t, fixtureDir)
			require.Equal(t, "passed", result["status"])
			material := latestCapture(t, fixtureDir)
			connections := stringsOf(material["connections"])
			refs := array(result["executions"])
			require.Len(t, refs, 1)
			require.Len(t, array(obj(refs[0])["connections"]), len(connections))
			if len(connections) > 1 {
				recordRoot := filepath.Join(fixtureDir, "packages", str(material["id"]), "record", str(result["id"]))
				report := readJSON(t, filepath.Join(recordRoot, "report.json"))
				require.Equal(t, previous, report["currentAfterMain"])
				require.Equal(t, false, report["mainResultHasMaterial"])
				materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
				require.FileExists(t, filepath.Join(materialRoot, "remote-probe", "raw", "raw.jsonl"))
				require.Contains(t, obj(readJSON(t, filepath.Join(materialRoot, "manifest.json"))["files"]), "remote-probe/raw/raw.jsonl")
			}
			require.NotContains(t, string(readAllFiles(t, fixtureDir)), remote.token)
			require.Len(t, array(fixtureIndex(t, fixtureDir)["executions"]), 1)
			remote.assertAlive(t)
			if target == "owned" {
				require.Equal(t, true, obj(result["remoteService"])["owned"])
				require.Equal(t, true, obj(result["remoteService"])["cleanupComplete"])
			}
		})
	}
}

func waitDriverCancellation() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(ch)
	<-ch
}

// A native service substitute exercises ownership and two real WebSocket
// connections without invoking an installed Agent or an SDK.
func controlledNativeService(endpoint, tokenFile string) int {
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return 2
	}
	var config net.ListenConfig
	listener, err := config.Listen(context.Background(), "tcp", strings.TrimPrefix(endpoint, "ws://"))
	if err != nil {
		return 2
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.TrimSpace(string(token)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			kind, _, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), kind, []byte(remoteReply)); err != nil {
				return
			}
		}
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	<-ch
	_ = server.Close()
	<-done
	return 0
}

func driverCheckpoint(output, stage string) {
	_ = writeJSONFile(filepath.Join(output, "checkpoint.json"), object{"pid": os.Getpid(), "stage": stage})
}
