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
	var request struct {
		conformanceDriverRequest
		Peer struct {
			conformanceCommand
			Probe *conformanceCommand `json:"probe"`
		} `json:"peer"`
	}
	if readJSONFile(args[3], &request) != nil {
		return 2
	}
	var plan object
	if readJSONFile(filepath.Join(request.Materials, "case.json"), &plan) != nil {
		return 2
	}
	if mode == "cancel-before" {
		waitDriverCancellation()
		return 0
	}
	report := object{"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"], "status": "passed", "branchStatus": "passed", "gaps": []any{}, "captureStatus": "complete", "officialSDK": object{"package": "openai-codex", "version": "synthetic-official-driver"}}
	if mode == "busy" {
		report["finishBefore"] = driverFinish(request.Output, args[1])
	}
	if mode == "busy" && plan["_replay"] == true {
		report["verifyBefore"] = driverAction(request.Output, args[1], "official", "verify")
		report["recordBefore"] = driverAction(request.Output, args[1], "agent", "record", "--cmd", "/usr/bin/false", "--", "app-server", "--listen", "stdio://")
		report["officialRecordBefore"] = driverAction(request.Output, args[1], "official", "record", "--cmd", "/usr/bin/false", "--", "app-server", "--listen", "stdio://")
	}
	report["host"] = object{"home": os.Getenv("HOME"), "config": os.Getenv("CODEX_HOME"), "workspace": request.Workspace}

	if mode != "no-trigger" && mode != "reuse-ref" {
		peers := []conformanceCommand{request.Peer.conformanceCommand}
		if request.Peer.Probe != nil {
			peers = append(peers, *request.Peer.Probe)
		}
		for i, peer := range peers {
			cmd := exec.CommandContext(context.Background(), peer.Command, peer.Args...)
			cmd.Dir = request.Workspace
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
			if mode == "cancel-during" {
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
				if readJSONFile(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(request.Output))), "current.json"), &finished) != nil {
					return 5
				}
				report["currentAfterMain"] = finished["materialId"]
				notices, err := os.ReadFile(filepath.Join(request.Output, "peer-main.results.jsonl"))
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

	if mode == "reuse-ref" {
		session := filepath.Dir(filepath.Dir(filepath.Dir(request.Output)))
		var paths map[string]string
		if readJSONFile(filepath.Join(session, "reuse-reference.json"), &paths) != nil {
			return 6
		}
		for name, source := range paths {
			data, err := os.ReadFile(source)
			if err != nil || os.WriteFile(filepath.Join(request.Output, name), data, 0o600) != nil {
				return 6
			}
		}
	}
	if mode == "busy" {
		report["finishAfter"] = driverFinish(request.Output, args[1])
	}
	if mode == "business-failed" {
		report["status"], report["branchStatus"] = "failed", "failed"
	}
	if mode == "wrong-ref" {
		path := filepath.Join(request.Output, "peer-main.results.jsonl")
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
	reportName := "report.json"
	if plan["_replay"] == true {
		reportName = "replay-report.json"
	}
	if writeJSONFile(filepath.Join(request.Output, reportName), report) != nil {
		return 7
	}
	if writeJSONFile(filepath.Join(request.Output, "result.json"), object{"status": report["status"], "sdk": report["officialSDK"]}) != nil {
		return 7
	}
	return 0
}

func driverFinish(output, binary string) int {
	return driverAction(output, binary, "finish")
}

func driverAction(output, binary string, args ...string) int {
	session := filepath.Dir(filepath.Dir(filepath.Dir(output)))
	// Session flags precede native argv, exactly as the installed CLI requires.
	offset := len(args)
	for i, arg := range args {
		if arg == "--" {
			offset = i
			break
		}
	}
	argv := append([]string{}, args[:offset]...)
	argv = append(argv, "--session-dir", session)
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

func officialRunResult(t *testing.T, session string) object {
	t.Helper()
	var ids []string
	require.NoError(t, readJSONFile(filepath.Join(session, "official-records", "index.json"), &ids))
	require.Len(t, ids, 1)
	return readJSON(t, filepath.Join(session, "official-records", ids[0], "result.json"))
}

func TestOfficialRecordInstalledChain(t *testing.T) {
	for _, mode := range []string{"local", "busy", "business-failed", "no-trigger", "wrong-ref", "cancel-before", "cancel-during", "startup-failed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			agent, drivers, env := isolatedCommands(t, root, "codex")
			// The driver is explicitly synthetic. No official SDK is installed.
			writeExecutable(t, filepath.Join(drivers, "agent-sdk-testify-codex"), os.Executable, "conformance-official-driver", mode, os.Getenv("AGENT_SDK_TESTIFY_BIN"))
			if mode == "startup-failed" {
				require.NoError(t, os.WriteFile(filepath.Join(drivers, "agent-sdk-testify-codex"), []byte("#!/missing/interpreter\n"), 0o700))
			}
			session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			source := obj(readJSON(t, filepath.Join(session, "session.json"))["source"])
			sourceBytes := readFile(t, str(source["path"]))
			previous := currentMaterialID(t, session)
			args := []string{"official", "record", "--session-dir", session}
			if strings.HasPrefix(mode, "cancel-") {
				args = append(args, "--timeout", "1s")
			}
			args = append(args, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			command := runTool(t, env, t.TempDir(), "", args...)
			result := officialRunResult(t, session)
			if mode == "local" || mode == "busy" {
				require.Zero(t, command.code, command.stderr)
				require.Equal(t, "passed", result["status"])
			} else {
				require.Equal(t, 1, command.code, command.stderr)
				require.Equal(t, "failed", result["status"])
			}
			require.Equal(t, sourceBytes, readFile(t, str(source["path"])))
			require.NoFileExists(t, filepath.Join(session, "official-active.json"))
			if mode == "no-trigger" || mode == "cancel-before" || mode == "startup-failed" {
				require.Empty(t, array(sessionIndex(t, session)["executions"]))
				require.Equal(t, previous, currentMaterialID(t, session))
				return
			}
			require.Len(t, array(sessionIndex(t, session)["executions"]), 1)
			if mode == "cancel-during" {
				require.Equal(t, previous, currentMaterialID(t, session))
				return
			}
			material := latestCapture(t, session)
			require.NotEqual(t, previous, material["id"])
			require.Equal(t, "official", material["provenance"])
			require.NotEmpty(t, obj(material["officialOrigin"])["runId"])
			materialRoot := str(material["path"])
			require.Equal(t, sourceBytes, readFile(t, filepath.Join(materialRoot, "source-plan.json")))
			assertFileHashes(t, materialRoot, obj(material["files"]))
			require.Equal(t, "captured", readJSON(t, filepath.Join(materialRoot, "report.json"))["status"])
			require.NoFileExists(t, filepath.Join(materialRoot, "result.json"))
			if mode == "busy" {
				require.EqualValues(t, 1, obj(result["report"])["finishBefore"])
				require.EqualValues(t, 1, obj(result["report"])["finishAfter"])
			}
			if mode == "local" {
				plan := readJSON(t, filepath.Join(materialRoot, "plan.json"))
				payload, marshalErr := json.Marshal(object{"id": 1, "method": "conformance", "params": object{"cwd": plan["project"]}})
				require.NoError(t, marshalErr)
				replayed := runPreparedMock(t, env, session, "main", string(payload)+"\n")
				require.Contains(t, replayed.stdout, `"synthetic":true`)
				exported := filepath.Join(t.TempDir(), "exported")
				mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", exported)
				originalProof := readFile(t, filepath.Join(exported, "official-origin.json"))
				// Removing the original session makes hidden old-session lookups fail.
				require.NoError(t, os.RemoveAll(filepath.Join(session, "official-records")))
				imported := prepareSession(t, env, "--recording", exported)
				importedMaterial := latestCapture(t, imported)
				require.Equal(t, originalProof, readFile(t, filepath.Join(str(importedMaterial["path"]), "official-origin.json")))
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
			session := prepareSession(t, env, "--agent", "codex", "--case", caseID, "--model", "synthetic-model")
			previous := currentMaterialID(t, session)
			remote := newControlledRemote(t)
			args := append([]string{"official", "record", "--session-dir", session}, remote.args("/success")...)
			if target == "owned" {
				args = []string{"official", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://"}
			}
			command := runTool(t, env, t.TempDir(), "", args...)
			require.Zero(t, command.code, command.stderr)
			result := officialRunResult(t, session)
			require.Equal(t, "passed", result["status"])
			material := latestCapture(t, session)
			connections := stringsOf(material["connections"])
			require.Len(t, array(result["connections"]), len(connections))
			if len(connections) > 1 {
				require.Equal(t, previous, obj(result["report"])["currentAfterMain"])
				require.Equal(t, false, obj(result["report"])["mainResultHasMaterial"])
				require.FileExists(t, filepath.Join(str(material["path"]), "remote-probe", "raw", "raw.jsonl"))
				require.NotEmpty(t, obj(readJSON(t, filepath.Join(str(material["path"]), "manifest.json"))["remotePhaseFiles"]))
			}
			require.NotContains(t, string(readAllFiles(t, session)), remote.token)
			require.Len(t, array(sessionIndex(t, session)["executions"]), 1)
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
