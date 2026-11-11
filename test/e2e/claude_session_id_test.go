package conformance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const syntheticSessionID = "de992cae-0d67-4ebe-93ee-182876c87fd5"

func assignedSessionArgs() []string {
	// The consumer SDK uses equals spelling for caller-assigned identities.
	return []string{
		"--output-format", "stream-json", "--verbose", "--input-format", "stream-json",
		"--permission-prompt-tool", "stdio", "--include-partial-messages",
		"--model=synthetic-model", "--session-id=" + syntheticSessionID,
	}
}

func assignedSessionInput(requestID string) string {
	return `{"type":"control_request","request_id":"` + requestID + `","request":{"subtype":"initialize","forwardSubagentText":true}}` + "\n" +
		`{"type":"user","session_id":"` + syntheticSessionID + `","message":{"role":"user","content":"synthetic prompt"},"parent_tool_use_id":null,"uuid":"11111111-1111-4111-8111-111111111111","unknown":9007199254740993}` + "\n"
}

// This child replaces only the native/model dependency. Capture, replay, export
// and execution finalization all run through the installed public CLI.
func syntheticAssignedSession(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("synthetic-assigned-session")
		return 0
	}
	var sessionID string
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--session-id="); ok {
			sessionID = value
		}
	}
	if sessionID == "" {
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 3
	}
	observed, err := json.Marshal(args)
	if err != nil || os.WriteFile(filepath.Join(os.Getenv("HOME"), "synthetic-native-args.json"), observed, 0o600) != nil {
		return 3
	}
	input := bufio.NewReader(os.Stdin)
	for {
		line, err := input.ReadBytes('\n')
		if err == io.EOF {
			return 0
		}
		if err != nil {
			return 4
		}
		var request struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(line, &request) != nil {
			return 5
		}
		switch request.Type {
		case "control_request":
			fmt.Fprintf(os.Stdout, `{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{"commands":[],"models":[]}}}`+"\n", request.RequestID)
		case "user":
			fmt.Fprintf(os.Stdout, `{"type":"system","subtype":"init","session_id":%q,"cwd":%q,"synthetic":true}`+"\n", sessionID, cwd)
			fmt.Fprintf(os.Stdout, `{"type":"assistant","session_id":%q,"parent_tool_use_id":null,"message":{"id":"synthetic-message","type":"message","role":"assistant","model":"synthetic-model","content":[{"type":"text","text":"synthetic answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n", sessionID)
			fmt.Fprintf(os.Stdout, `{"type":"result","subtype":"success","session_id":%q,"is_error":false,"result":"synthetic answer","duration_ms":1,"duration_api_ms":1,"num_turns":1,"total_cost_usd":0,"usage":{"input_tokens":1,"output_tokens":1}}`+"\n", sessionID)
		default:
			return 6
		}
	}
}

func TestClaudeAssignedSessionRecordReplayExport(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "claude")
	writeExecutable(t, agent, os.Executable, "synthetic-assigned-session")
	planPath := writeConsumerPlan(t, root, "synthetic-assigned-session")
	plan := readJSON(t, planPath)
	plan["agentType"] = "claude"
	plan["cliVersion"] = "synthetic-assigned-session"
	writeJSON(t, planPath, plan)
	fixture := prepareFixture(t, env, "--plan", planPath)

	args := assignedSessionArgs()
	recordArgs := append([]string{"--cmd", agent, "--"}, args...)
	recorded, recordRef := runAgentResult(t, env, fixture, assignedSessionInput("record-init"), "record", recordArgs...)
	require.Zero(t, recorded.code, recorded.stderr)
	require.Equal(t, "passed", recordRef["status"])
	recordExecution := readJSON(t, str(recordRef["execution"]))
	var observed []string
	require.NoError(t, json.Unmarshal(readFile(t, filepath.Join(str(obj(recordExecution["env"])["HOME"]), "synthetic-native-args.json")), &observed))
	require.Equal(t, args, observed)
	require.Equal(t, true, readJSON(t, str(recordRef["receipt"]))["cleanupComplete"])
	require.Equal(t, "passed", connectionStatus(t, fixture, recordExecution, "main"))
	for _, line := range strings.Split(strings.TrimSpace(recorded.stdout), "\n")[1:] {
		var event object
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		require.Equal(t, syntheticSessionID, event["session_id"])
	}
	material := str(obj(recordRef["material"])["path"])
	require.Equal(t, "consumer", obj(recordRef["material"])["provenance"])
	hashes := packageHashes(t, material)
	assertFileHashes(t, material, obj(readJSON(t, filepath.Join(material, "material.json"))["files"]))

	for range 2 {
		replayed, reference := runAgentResult(t, env, fixture, assignedSessionInput("replay-init"), "mock", append([]string{"--"}, args...)...)
		require.Zero(t, replayed.code, replayed.stderr)
		require.Equal(t, strings.ReplaceAll(recorded.stdout, "record-init", "replay-init"), replayed.stdout)
		require.Equal(t, "passed", reference["status"])
		execution := readJSON(t, str(reference["execution"]))
		require.Equal(t, args, stringsOf(execution["nativeArgs"]))
		require.NotEqual(t, recordRef["executionId"], reference["executionId"])
		receipt := readJSON(t, str(reference["receipt"]))
		for _, key := range []string{"fullConsumption", "clientEOF", "cleanupComplete"} {
			require.Equal(t, true, receipt[key], key)
		}
		require.Equal(t, false, receipt["nativeLaunched"])
	}

	for _, mutation := range []struct{ name, old, replacement string }{
		{"session", syntheticSessionID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		{"uuid", "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"},
		{"unknown field", "9007199254740993", "9007199254740992"},
		{"prompt", "synthetic prompt", "different prompt"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			input := strings.ReplaceAll(assignedSessionInput("replay-init"), mutation.old, mutation.replacement)
			result, reference := runAgentResult(t, env, fixture, input, "mock", append([]string{"--"}, args...)...)
			require.Equal(t, 1, result.code, result.stderr)
			require.Equal(t, "failed", reference["status"])
			require.Contains(t, result.stderr, "client mismatch")
			require.Equal(t, true, readJSON(t, str(reference["receipt"]))["cleanupComplete"])
		})
	}

	// Representative malformed/unknown declarations fail before execution.
	// Spelling and duplicate combinations are covered by the parser unit tests.
	for _, bad := range [][]string{
		{"--session-id="},
		{"--session-id=" + syntheticSessionID, "--unknown=true"},
	} {
		for _, action := range []string{"record", "mock"} {
			before := readFixtureEvidence(t, fixture)
			startup := []string{"agent", action, "--fixture-dir", fixture}
			if action == "record" {
				startup = append(startup, "--cmd", agent)
			}
			startup = append(append(startup, "--"), bad...)
			result := runTool(t, env, root, "", startup...)
			require.Equal(t, 1, result.code, result.stderr)
			require.Contains(t, result.stderr, "native startup")
			require.Equal(t, before, readFixtureEvidence(t, fixture))
		}
	}
	require.Equal(t, hashes, packageHashes(t, material))
	exported := filepath.Join(root, "exported")
	mustRun(t, env, root, "", "export", "--fixture-dir", fixture, "--output", exported)
	require.Equal(t, hashes, packageHashes(t, exported))
	imported := prepareFixture(t, env, "--recording", exported)
	require.NoError(t, os.RemoveAll(fixture))
	require.NoError(t, os.RemoveAll(exported))
	replayed, reference := runAgentResult(t, env, imported, assignedSessionInput("import-init"), "mock", append([]string{"--"}, args...)...)
	require.Zero(t, replayed.code, replayed.stderr)
	require.Equal(t, strings.ReplaceAll(recorded.stdout, "record-init", "import-init"), replayed.stdout)
	require.Equal(t, "passed", reference["status"])
}
