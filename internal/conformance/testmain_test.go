package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

const conformanceRequest = `{"id":1,"method":"conformance","unknown":9007199254740993}` + "\n"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "conformance-official-driver":
			os.Exit(controlledOfficialDriver(os.Args[2:]))
		case "conformance-agent":
			os.Exit(conformanceAgent())
		case "conformance-driver":
			os.Exit(conformanceDriver(os.Args[2:]))
		}
	}
	cleanup, err := testutil.PrepareBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func conformanceAgent() int {
	if len(os.Args) > 2 && (os.Args[2] == "--version" || os.Args[2] == "-v") {
		fmt.Println("synthetic-conformance-native")
		return 0
	}
	endpoint, tokenFile := "", ""
	for i, arg := range os.Args[:len(os.Args)-1] {
		if arg == "--listen" && strings.HasPrefix(os.Args[i+1], "ws://") {
			endpoint = os.Args[i+1]
		}
		if arg == "--ws-token-file" {
			tokenFile = os.Args[i+1]
		}
	}
	if endpoint != "" {
		return controlledNativeService(endpoint, tokenFile)
	}
	if observed := os.Getenv("OPENAI_API_KEY"); observed != "" {
		value := "absent\n"
		if observed == "conformance-runtime-secret" {
			value = "matched\n"
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "runtime-secret-observed"), []byte(value), 0o600); err != nil {
			return 3
		}
	}
	config := os.Getenv("CODEX_HOME")
	if config != "" {
		history := filepath.Join(config, "sessions", "conformance-history.jsonl")
		if err := os.MkdirAll(filepath.Dir(history), 0o700); err != nil {
			return 3
		}
		if err := os.WriteFile(history, []byte("{\"type\":\"conformance-history\"}\n"), 0o600); err != nil {
			return 3
		}
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return conformanceRecordFault()
		}
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &request) != nil {
			return 2
		}
		if len(request.ID) != 0 {
			fmt.Fprintf(os.Stdout, "{\"id\":%s,\"result\":{\"synthetic\":true}}\n", request.ID)
		}
	}
}

type conformanceCommand struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type conformanceDriverRequest struct {
	Case      string             `json:"case"`
	Materials string             `json:"materials"`
	Workspace string             `json:"workspace"`
	Peer      conformanceCommand `json:"peer"`
	Output    string             `json:"output"`
}

func conformanceDriver(args []string) int {
	if len(args) != 3 || args[1] != "--request" {
		return 2
	}
	mode := args[0]
	var req conformanceDriverRequest
	if readJSONFile(args[2], &req) != nil {
		return 2
	}
	var plan map[string]any
	if readJSONFile(filepath.Join(req.Materials, "case.json"), &plan) != nil {
		return 2
	}
	if mode == "record" {
		configKey := "CODEX_HOME"
		if plan["provider"] == "claude" {
			configKey = "CLAUDE_CONFIG_DIR"
		}
		if os.Getenv("HOME") != filepath.Join(req.Output, "home") || os.Getenv(configKey) != filepath.Join(req.Output, "config") {
			return 3
		}
	}
	cmd := exec.CommandContext(context.Background(), req.Peer.Command, req.Peer.Args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return 4
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 4
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return 4
	}
	if _, err := fmt.Fprint(in, conformanceRequest); err != nil {
		return 4
	}
	_ = in.Close()
	if _, err := bufio.NewReader(out).ReadBytes('\n'); err != nil {
		return 5
	}
	if err := cmd.Wait(); err != nil {
		return 5
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 6
	}
	sdkPackage := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[fmt.Sprint(plan["provider"])]
	report := map[string]any{
		"provider":      plan["provider"],
		"caseId":        plan["caseId"],
		"storyId":       plan["storyId"],
		"status":        "passed",
		"branchStatus":  "passed",
		"captureStatus": "complete",
		"gaps":          []any{},
		"officialSDK":   map[string]any{"package": sdkPackage, "version": "conformance-test-only"},
		"observed": map[string]any{
			"cwd":       cwd,
			"workspace": req.Workspace,
			"home":      os.Getenv("HOME"),
			"config":    os.Getenv("CODEX_HOME"),
			"tmp":       os.Getenv("TMPDIR"),
		},
	}
	name := "report.json"
	if mode == "verify" || mode == "verify-cleanup-failure" {
		name = "replay-report.json"
	}
	if writeJSONFile(filepath.Join(req.Output, name), report) != nil {
		return 6
	}
	if writeJSONFile(filepath.Join(req.Output, "result.json"), map[string]any{"status": "passed"}) != nil {
		return 6
	}
	if mode == "verify-cleanup-failure" {
		slot := filepath.Join(filepath.Dir(filepath.Dir(req.Output)), "routes", "verify", "workspace")
		if os.Remove(slot) != nil || os.Mkdir(slot, 0o700) != nil {
			return 7
		}
	}
	return 0
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Faults run inside the synthetic native process after its protocol exchange.
// They exercise publication and cleanup through the installed CLI boundary.
func conformanceRecordFault() int {
	for i, arg := range os.Args {
		if arg != "--conformance-fault" || i+2 >= len(os.Args) {
			continue
		}
		mode, session := os.Args[i+1], os.Args[i+2]
		var index struct {
			Executions []struct {
				ID string `json:"id"`
			} `json:"executions"`
		}
		if readJSONFile(filepath.Join(session, "executions", "index.json"), &index) != nil || len(index.Executions) == 0 {
			return 7
		}
		id := index.Executions[len(index.Executions)-1].ID
		var execution struct {
			Artifact struct {
				ID string `json:"id"`
			} `json:"artifact"`
		}
		if readJSONFile(filepath.Join(session, "executions", id, "execution.json"), &execution) != nil {
			return 7
		}
		var descriptor struct {
			ResourceRoot string `json:"resourceRoot"`
		}
		if readJSONFile(filepath.Join(session, "session.json"), &descriptor) != nil {
			return 7
		}
		switch mode {
		case "publication":
			if os.Mkdir(filepath.Join(session, "materials", execution.Artifact.ID), 0o700) != nil {
				return 7
			}
		case "current":
			if os.Chmod(session, 0o500) != nil {
				return 7
			}
		case "cleanup":
			slot := filepath.Join(descriptor.ResourceRoot, "routes", "record", "workspace")
			if os.Remove(slot) != nil || os.Mkdir(slot, 0o700) != nil {
				return 7
			}
		default:
			return 7
		}
	}
	return 0
}
