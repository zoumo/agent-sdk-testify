package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These processes replace only the installed official driver and native Agent.
// The installed tool, environment preparation and recording proxy are real.
func recordDriverSubstitute(args []string) int {
	status := args[0]
	if status == "hang" {
		time.Sleep(time.Minute)
		return 9
	}
	if status == "crash" {
		return 9
	}
	var req request
	if len(args) != 3 || args[1] != "--request" || readJSON(args[2], &req) != nil {
		return 2
	}
	var plan object
	if readJSON(filepath.Join(req.Materials, "case.json"), &plan) != nil {
		return 2
	}
	provider := str(plan["provider"])
	configKey := "CODEX_HOME"
	if provider == "claude" {
		configKey = "CLAUDE_CONFIG_DIR"
	}
	if os.Getenv("HOME") != filepath.Join(req.Output, "home") || os.Getenv(configKey) != filepath.Join(req.Output, "config") {
		return 3
	}
	if _, err := os.Stat(filepath.Join(req.Workspace, "numbers.txt")); err != nil {
		return 4
	}
	cmd := exec.Command(req.Peer.Command, req.Peer.Args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return 5
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 5
	}
	cmd.Stderr = os.Stderr
	if cmd.Start() != nil {
		return 5
	}
	fmt.Fprintln(in, `{"id":1,"method":"synthetic","unknown":9007199254740993}`)
	// End the request stream even when native startup fails before a response.
	_ = in.Close()
	_, readErr := bufio.NewReader(out).ReadBytes('\n')
	waitErr := cmd.Wait()
	if status != "false-pass" && (readErr != nil || waitErr != nil) {
		status = "failed"
	}
	if status == "false-pass" {
		status = "passed"
	}
	if writeJSON(filepath.Join(req.Output, "report.json"), object{"status": status, "branchStatus": status, "captureStatus": "synthetic-driver-source", "officialSDK": object{"version": "synthetic-only"}}) != nil {
		return 6
	}
	if writeJSON(filepath.Join(req.Output, "result.json"), result{Status: status, Message: "synthetic-only"}) != nil {
		return 6
	}
	return 0
}

func recordAgentSubstitute(args []string) int {
	if len(args) > 1 && args[1] == "--version" {
		fmt.Println("synthetic-native-only")
		return 0
	}
	if args[0] == "crash" {
		fmt.Fprintln(os.Stderr, "synthetic native crash")
		return 7
	}
	reader := bufio.NewReader(os.Stdin)
	if _, err := reader.ReadBytes('\n'); err != nil {
		return 2
	}
	fmt.Fprintln(os.Stdout, `{"id":1,"result":{"unknown":1.2300}}`)
	fmt.Fprintln(os.Stderr, "synthetic native stderr")
	for {
		if _, err := reader.ReadByte(); err != nil {
			return 0
		}
	}
}

func recordProcess(t *testing.T, provider, status, native string) (*exec.Cmd, string) {
	t.Helper()
	bin := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)
	_, err = writePeerLauncher(bin, "official", command{Command: self, Args: []string{"record-driver-substitute", status}})
	require.NoError(t, err)
	driverPath := filepath.Join(bin, "agent-sdk-testify-"+provider)
	require.NoError(t, os.Rename(filepath.Join(bin, "peer-official"), driverPath))
	for _, path := range []string{driverPath} {
		script, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(script[:len(script)-1], []byte(" \"$@\"\n")...), 0700))
	}
	cli, err := writePeerLauncher(bin, "native", command{Command: self, Args: []string{"record-agent-substitute", native}})
	require.NoError(t, err)
	script, err := os.ReadFile(cli)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cli, append(script[:len(script)-1], []byte(" \"$@\"\n")...), 0700))
	config := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(config, "AGENTS.md"), []byte("personal instructions must not be copied"), 0600))
	output := filepath.Join(t.TempDir(), "capture")
	cmd := exec.Command(os.Getenv("AGENT_SDK_TESTIFY_BIN"), "record", "--provider", provider, "--case", "SDK-CASE-001", "--cli", cli, "--config", config, "--output", output, "--timeout", "10s")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + bin, "GORACE=atexit_sleep_ms=0"}
	cmd.WaitDelay = 15 * time.Second
	return cmd, output
}

func TestRecordUsesInstalledOfficialDriverOutsideCheckout(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) { checkRecordCapture(t, provider) })
	}
}

func checkRecordCapture(t *testing.T, provider string) {
	cmd, output := recordProcess(t, provider, "passed", "passed")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var summary object
	require.NoError(t, json.Unmarshal(data, &summary))
	require.Equal(t, "passed", obj(summary["result"])["status"])
	var plan, manifest, transport, report object
	require.NoError(t, readJSON(filepath.Join(output, "plan.json"), &plan))
	require.Equal(t, "SDK-CASE-001", plan["caseId"])
	require.Equal(t, "continuation", plan["driverProfile"])
	require.Equal(t, "personal-skills-v1", plan["recordingIsolation"])
	require.NoError(t, readJSON(filepath.Join(output, "manifest.json"), &manifest))
	require.Equal(t, "passed", manifest["status"])
	require.Equal(t, "complete", manifest["captureStatus"])
	require.NoError(t, readJSON(filepath.Join(output, "raw/transport.json"), &transport))
	require.Equal(t, true, transport["cleanupComplete"])
	require.NoError(t, readJSON(filepath.Join(output, "report.json"), &report))
	require.Equal(t, "synthetic-driver-source", report["captureStatus"])
	raw, err := os.ReadFile(filepath.Join(output, "raw/raw.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "9007199254740993")
	require.Contains(t, string(raw), "1.2300")
	stderr, err := os.ReadFile(filepath.Join(output, "raw/stderr.raw"))
	require.NoError(t, err)
	require.Contains(t, string(stderr), "synthetic native stderr")
	require.NoFileExists(t, filepath.Join(output, "config/AGENTS.md"))
	require.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
	require.NoFileExists(t, filepath.Join(output, "review.json"))
}

func TestRecordPreservesFailedAttempts(t *testing.T) {
	for _, test := range []struct{ name, driver, native, status string }{
		{"business-failed", "failed", "passed", "failed"},
		{"untriggered", "untriggered", "passed", "untriggered"},
		{"unsupported", "unsupported", "passed", "unsupported"},
		{"driver-crash", "crash", "passed", ""},
		{"native-crash-after-business-pass", "false-pass", "crash", "passed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd, output := recordProcess(t, "codex", test.driver, test.native)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			var summary object
			require.NoError(t, readJSON(filepath.Join(output, "summary.json"), &summary))
			require.Equal(t, test.status, obj(summary["result"])["status"])
			require.FileExists(t, filepath.Join(output, "plan.json"))
			require.FileExists(t, filepath.Join(output, "stderr.log"))
			require.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
			if test.driver != "crash" {
				var manifest object
				require.NoError(t, readJSON(filepath.Join(output, "manifest.json"), &manifest))
				require.Equal(t, test.status, manifest["status"])
				require.FileExists(t, filepath.Join(output, "raw/raw.jsonl"))
			}
		})
	}
}

func TestRecordPreparationFailureKeepsDiagnostic(t *testing.T) {
	cmd, output := recordProcess(t, "claude", "passed", "passed")
	// The explicitly selected config must be readable; no personal fallback.
	config := filepath.Join(t.TempDir(), "invalid-config")
	require.NoError(t, os.WriteFile(config, []byte("not a directory"), 0600))
	cmd.Args = append(cmd.Args, "--config", config)
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	var summary object
	require.NoError(t, readJSON(filepath.Join(output, "summary.json"), &summary))
	require.NotEmpty(t, summary["error"])
	require.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
}

func TestRecordHelpAndSelectionErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		message string
		success bool
	}{
		{"help", []string{"--help"}, "case ID", true},
		{"arbitrary-driver", []string{"--driver", "/never-execute"}, "flag provided but not defined", false},
		{"provider", []string{"--provider", "other", "--case", "SDK-CASE-001", "--cli", "/never-execute"}, "unsupported provider", false},
		{"missing-official-driver", []string{"--provider", "codex", "--case", "SDK-CASE-001", "--cli", "/never-execute"}, "install the driver wheel", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Getenv("AGENT_SDK_TESTIFY_BIN"), append([]string{"record"}, test.args...)...)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=/missing"}
			data, err := cmd.CombinedOutput()
			if test.success {
				require.NoError(t, err, "%s", data)
			} else {
				require.Error(t, err)
			}
			require.Contains(t, string(data), test.message)
		})
	}
}

func TestRecordRejectsAbsentCatalogCase(t *testing.T) {
	cmd, output := recordProcess(t, "codex", "passed", "passed")
	cmd.Args = append(cmd.Args, "--case", "SDK-CASE-999")
	data, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(data), "absent from shared materials")
	require.NoDirExists(t, output)
}

func TestRecordTimeoutPreservesDiagnostic(t *testing.T) {
	cmd, output := recordProcess(t, "codex", "hang", "passed")
	cmd.Args = append(cmd.Args, "--timeout", "500ms")
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	var summary object
	require.NoError(t, readJSON(filepath.Join(output, "summary.json"), &summary))
	require.Contains(t, str(summary["error"]), "context deadline exceeded")
	require.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
}
