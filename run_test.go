package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "peer":
			if e := runPeer(os.Args[2:]); e != nil {
				fmt.Fprintln(os.Stderr, e)
				os.Exit(1)
			}
			os.Exit(0)
		case "protocol":
			main()
			os.Exit(0)
		case "verify-driver-substitute":
			os.Exit(verifyDriverSubstitute(os.Args[2:]))
		case "record-driver-substitute":
			os.Exit(recordDriverSubstitute(os.Args[2:]))
		case "record-agent-substitute":
			os.Exit(recordAgentSubstitute(os.Args[2:]))
		case "driver":
			os.Exit(testDriver(os.Args[2:]))
		case "--version":
			fmt.Println("native-test any-version")
			os.Exit(0)
		case "synthetic-agent":
			r := bufio.NewReader(os.Stdin)
			if _, e := r.ReadBytes('\n'); e != nil {
				os.Exit(1)
			}
			fmt.Println(`{"id":1,"result":{}}`)
			for {
				if _, e := r.ReadByte(); e != nil {
					break
				}
			}
			os.Exit(0)
		case "synthetic-remote-cleanup":
			interrupted := make(chan os.Signal, 1)
			signal.Notify(interrupted, os.Interrupt)
			fmt.Println("ready")
			<-interrupted
			os.Exit(0)
		}
	}
	// Default tool tests build their own peer; callers may supply an explicit
	// binary to test an installed artifact through the same process boundary.
	var buildDir string
	if os.Getenv("AGENT_SDK_TESTIFY_BIN") == "" {
		var err error
		buildDir, err = os.MkdirTemp("", "agent-sdk-testify-test-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		binary := filepath.Join(buildDir, "agent-sdk-testify")
		output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build test tool: %v\n%s", err, output)
			_ = os.RemoveAll(buildDir)
			os.Exit(1)
		}
		if err := os.Setenv("AGENT_SDK_TESTIFY_BIN", binary); err != nil {
			fmt.Fprintln(os.Stderr, err)
			_ = os.RemoveAll(buildDir)
			os.Exit(1)
		}
	}
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func testDriver(args []string) int {
	if len(args) != 2 || args[0] != "--request" {
		return 2
	}
	var r request
	if readJSON(args[1], &r) != nil {
		return 2
	}
	if r.Case == "crash" {
		return 9
	}
	if r.Case == "bad-result" {
		_ = os.WriteFile(filepath.Join(r.Output, "result.json"), []byte(`{`), 0600)
		return 0
	}
	cmd := exec.Command(r.Peer.Command, r.Peer.Args...)
	in, e := cmd.StdinPipe()
	if e != nil {
		return 2
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		return 2
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		return 2
	}
	if r.Case == "cancel" {
		time.Sleep(time.Minute)
		return 2
	}
	_, _ = fmt.Fprintln(in, `{"id":1,"method":"ping"}`)
	_, e = bufio.NewReader(out).ReadBytes('\n')
	_ = in.Close()
	waitErr := cmd.Wait()
	status := "passed"
	if e != nil || waitErr != nil || r.Case == "failed" {
		status = "failed"
	}
	if r.Case == "false-pass" {
		status = "passed"
		_ = os.Remove(filepath.Join(r.Output, "replay-peer.json"))
	}
	if r.Case == "unsupported" || r.Case == "untriggered" {
		status = r.Case
		_ = os.Remove(filepath.Join(r.Output, "replay-peer.json"))
	}
	field := map[string]string{"wrong-hash": "recordingSHA256", "wrong-provider": "provider", "not-consumed": "fullConsumption", "no-eof": "clientEOF", "native-launched": "nativeLaunched"}[r.Case]
	if field != "" {
		path := filepath.Join(r.Output, "replay-peer.json")
		var receipt object
		if readJSON(path, &receipt) != nil {
			return 2
		}
		switch field {
		case "recordingSHA256", "provider":
			receipt[field] = "wrong"
		case "nativeLaunched":
			receipt[field] = true
		default:
			receipt[field] = false
		}
		if writeJSON(path, receipt) != nil {
			return 2
		}
	}
	var plan object
	if readJSON(filepath.Join(r.Materials, "case.json"), &plan) != nil {
		return 2
	}
	if plan["corpusTestBusiness"] == true {
		name := "report.json"
		if _, err := os.Stat(filepath.Join(r.Output, "replay-peer.json")); err == nil {
			name = "replay-report.json"
			if plan["corpusTestReplayFail"] == true {
				status = "failed"
			}
		}
		if plan["corpusTestFail"] == true {
			status = "failed"
		}
		business := object{"status": status, "branchStatus": status, "captureStatus": "complete",
			"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"],
			"officialSDK": object{"package": "openai-codex", "version": "synthetic-test-only"}}
		if writeJSON(filepath.Join(r.Output, name), business) != nil {
			return 2
		}
	}
	if os.Getenv("HOME") == "" || os.Getenv("CODEX_HOME") == "" {
		return 2
	}
	if writeJSON(filepath.Join(r.Output, "result.json"), result{Status: status}) != nil {
		return 2
	}
	return 0
}

func sample(t *testing.T, id string) options {
	t.Helper()
	source := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(source, "raw"), 0700))
	frames := []object{{"ordinal": 1, "direction": "c2s", "channel": "protocol", "epoch": 1, "elapsedMs": 0, "wire": object{"id": 1, "method": "ping"}},
		{"ordinal": 2, "direction": "s2c", "channel": "protocol", "epoch": 1, "elapsedMs": 1, "wire": object{"id": 1, "result": object{}}}}
	var data []byte
	for _, f := range frames {
		b, e := json.Marshal(f)
		require.NoError(t, e)
		data = append(append(data, b...), '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(source, "raw/raw.jsonl"), data, 0600))
	require.NoError(t, writeJSON(filepath.Join(source, "plan.json"), object{"provider": "codex", "caseId": id, "project": filepath.Join(source, "original-cwd"), "driverProfile": "continuation", "args": []string{"synthetic-agent"}, "scenario": object{"files": object{"numbers.txt": "7\n11\n"}}, "agentMock": object{"sha256": "deliberately-different", "version": "old-version"}}))
	peer := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	require.NotEmpty(t, peer, "run with scripts/with-agent-sdk-testify.sh")
	self, e := os.Executable()
	require.NoError(t, e)
	return options{Mode: "replay", Materials: source, Driver: self, DriverArgs: []string{"driver"}, Peer: peer, Output: filepath.Join(t.TempDir(), "run")}
}

func TestDriverContract(t *testing.T) {
	for _, id := range []string{"passed", "failed", "crash", "bad-result", "false-pass", "wrong-hash", "wrong-provider", "not-consumed", "no-eof", "native-launched", "unsupported", "untriggered"} {
		t.Run(id, func(t *testing.T) {
			o := sample(t, id)
			before, e := os.ReadFile(filepath.Join(o.Materials, "raw/raw.jsonl"))
			require.NoError(t, e)
			summary, e := run(context.Background(), o)
			if id == "passed" {
				if e != nil {
					for _, name := range []string{"stderr.log", "raw/transport.json"} {
						b, _ := os.ReadFile(filepath.Join(o.Output, name))
						t.Log(name, string(b))
					}
				}
				require.NoError(t, e)
				require.Equal(t, "passed", summary["result"].(result).Status)
			} else {
				require.Error(t, e)
				if id == "unsupported" {
					require.ErrorContains(t, e, "driver reported unsupported")
					require.NotContains(t, e.Error(), "peer:")
				} else if id == "untriggered" {
					require.ErrorContains(t, e, "main peer:")
				} else if id != "failed" && id != "crash" && id != "bad-result" {
					require.Equal(t, "passed", summary["result"].(result).Status)
					require.ErrorContains(t, e, "main peer:")
				}
			}
			after, e := os.ReadFile(filepath.Join(o.Materials, "raw/raw.jsonl"))
			require.NoError(t, e)
			require.Equal(t, before, after)
			var plan object
			require.NoError(t, readJSON(filepath.Join(o.Output, "materials/case.json"), &plan))
			require.Equal(t, filepath.Join(o.Materials, "original-cwd"), plan["project"])
			require.FileExists(t, filepath.Join(o.Output, "project/numbers.txt"))
		})
	}
}

func TestUnselectedPeerMaterialsDoNotRequireReceipt(t *testing.T) {
	o := sample(t, "passed")
	probe := filepath.Join(o.Materials, "remote-probe/raw")
	require.NoError(t, os.MkdirAll(probe, 0700))
	require.NoError(t, copyFile(filepath.Join(o.Materials, "raw/raw.jsonl"), filepath.Join(probe, "raw.jsonl")))
	_, err := run(context.Background(), o)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(o.Output, "remote-probe/replay-peer.json"))
}

func TestCancellation(t *testing.T) {
	o := sample(t, "cancel")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := run(ctx, o)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestCaptureWithSyntheticNative(t *testing.T) {
	o := sample(t, "passed")
	o.Mode = "record"
	self, e := os.Executable()
	require.NoError(t, e)
	o.CLI = self
	summary, e := run(context.Background(), o)
	if e != nil {
		for _, name := range []string{"stderr.log", "raw/transport.json"} {
			b, _ := os.ReadFile(filepath.Join(o.Output, name))
			t.Log(name, string(b))
		}
	}
	require.NoError(t, e)
	require.Equal(t, "passed", summary["result"].(result).Status)
	require.FileExists(t, filepath.Join(o.Output, "raw/transport.json"))
	require.NoFileExists(t, filepath.Join(o.Output, "native.env.private.json"))
	// The recording output itself is the next stage's material directory.
	replay := o
	replay.Mode = "replay"
	replay.Materials = o.Output
	replay.Output = filepath.Join(t.TempDir(), "replay")
	validated, e := run(context.Background(), replay)
	require.NoError(t, e)
	for _, name := range []string{"plan.json", "manifest.json"} {
		digest, err := fileDigest(filepath.Join(replay.Materials, name))
		require.NoError(t, err)
		require.Equal(t, digest, obj(validated["source"])[name])
	}
}

func TestSharedMaterialsContainNoExecutableSteps(t *testing.T) {
	o := options{Materials: "tests/simulation/scenarios", Case: "SDK-CASE-008", Provider: "claude"}
	p, e := loadMaterials(o)
	require.NoError(t, e)
	require.NotContains(t, obj(p["scenario"]), "steps")
	require.Equal(t, "approval", p["driverProfile"])
	o.Materials = t.TempDir()
	require.NoError(t, writeJSON(filepath.Join(o.Materials, "case.json"), object{"scenario": object{"files": object{"../outside": "bad"}}}))
	p, e = loadMaterials(o)
	require.NoError(t, e)
	require.ErrorContains(t, writeFiles(t.TempDir(), obj(obj(p["scenario"])["files"])), "escapes")
}

func TestCLIRejectsMissingSelection(t *testing.T) {
	var e = execute(context.Background(), []string{"replay"})
	require.Error(t, e)
	require.True(t, strings.Contains(e.Error(), "materials"))
}

func TestBatchSelectsDriversAndPreservesFailures(t *testing.T) {
	first, second := sample(t, "passed"), sample(t, "failed")
	selection := filepath.Join(t.TempDir(), "cases.json")
	require.NoError(t, writeJSON(selection, []object{
		{"case": first.Materials, "driver": command{Command: first.Driver, Args: first.DriverArgs}},
		{"case": second.Materials, "driver": command{Command: second.Driver, Args: second.DriverArgs}},
	}))
	o := options{Mode: "replay", CasesFile: selection, Peer: first.Peer, Output: filepath.Join(t.TempDir(), "batch"), Jobs: 2}
	summary, err := runBatch(context.Background(), o)
	require.ErrorContains(t, err, "1/2 cases passed")
	require.Equal(t, 1, summary["passed"])
	require.FileExists(t, filepath.Join(o.Output, "codex-failed/result.json"))
	require.FileExists(t, filepath.Join(o.Output, "summary.json"))
}
