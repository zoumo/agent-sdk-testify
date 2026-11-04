package conformance

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFinishBusyDoesNotWriteOrClean(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	file, err := os.OpenFile(filepath.Join(session, "locks", "action.lock"), os.O_RDWR, 0)
	require.NoError(t, err)
	defer file.Close()
	require.NoError(t, syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))
	before := readAllFiles(t, session)
	runtimeBefore := readAllFiles(t, str(descriptor["resourceRoot"]))
	failed := runTool(t, env, root, "", "finish", "--session-dir", session)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "session action busy")
	require.Equal(t, before, readAllFiles(t, session))
	require.Equal(t, runtimeBefore, readAllFiles(t, str(descriptor["resourceRoot"])))
	require.NoFileExists(t, filepath.Join(session, "closed"))
	require.NoError(t, syscall.Flock(int(file.Fd()), syscall.LOCK_UN))
	finished, _ := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
}

func TestFinishCleanupRetryPreservesFinalEvidence(t *testing.T) {
	for _, failedReplay := range []bool{false, true} {
		name := "passed"
		if failedReplay {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			agent, _, env := isolatedCommands(t, root, "codex")
			session := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "finish-retry"))
			recordConsumer(t, env, session, agent, requestOne)
			request := requestOne
			if failedReplay {
				request = `{"id":1,"method":"wrong"}` + "\n"
			}
			mock := runTool(t, env, root, request, "mock", "--session-dir", session)
			require.Equal(t, failedReplay, mock.code != 0)
			descriptor := readJSON(t, filepath.Join(session, "session.json"))
			resource := str(descriptor["resourceRoot"])
			owner := filepath.Join(resource, ".owner")
			require.NoError(t, os.WriteFile(owner, []byte("different-session"), 0o600))
			failed, report := finishSession(t, env, session)
			require.NotZero(t, failed.code)
			require.Equal(t, false, report["cleanupComplete"])
			require.Equal(t, name, report["validationStatus"])
			require.FileExists(t, filepath.Join(session, "closed"))
			require.DirExists(t, resource)
			evidence := report["executions"]
			before := readAllFiles(t, session)
			for _, action := range []string{"record", "verify", "mock"} {
				args := []string{action, "--session-dir", session}
				if action == "record" {
					args = append(args, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
				}
				blocked := runTool(t, env, root, "", args...)
				require.NotZero(t, blocked.code)
				require.Contains(t, blocked.stderr, "finished")
				require.Equal(t, before, readAllFiles(t, session))
			}
			mustRun(t, env, root, "", "export", "--session-dir", session, "--output", filepath.Join(root, "export-closed"))
			require.Equal(t, before, readAllFiles(t, session))
			execution := latestExecution(t, session, "mock")
			receipt := str(obj(array(execution["connections"])[0])["receipt"])
			// Retry must not reinterpret repaired or newly damaged action evidence.
			require.NoError(t, os.WriteFile(receipt, []byte("changed after final validation\n"), 0o600))
			require.NoError(t, os.WriteFile(owner, []byte(str(descriptor["sessionId"])), 0o600))
			retried, final := finishSession(t, env, session)
			require.Equal(t, failedReplay, retried.code != 0, retried.stderr)
			require.Equal(t, name, final["status"])
			require.Equal(t, evidence, final["executions"])
			require.Equal(t, true, final["cleanupComplete"])
			require.NoDirExists(t, resource)
			require.FileExists(t, filepath.Join(session, "locks", "action.lock"))
			require.Equal(t, "changed after final validation\n", string(readFile(t, receipt)))
			mustRun(t, env, root, "", "export", "--session-dir", session, "--output", filepath.Join(root, "export-finished"))
			stable := readAllFiles(t, session)
			repeated, finalAgain := finishSession(t, env, session)
			require.Equal(t, retried.code, repeated.code)
			require.Equal(t, final, finalAgain)
			require.Equal(t, stable, readAllFiles(t, session))
		})
	}
}

func TestFinishWhileMockAwaitsEOFLeavesStateUntouched(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "finish-active-mock"))
	recordConsumer(t, env, session, agent, requestOne)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), "mock", "--session-dir", session)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	_, err = stdin.Write([]byte(requestOne))
	require.NoError(t, err)
	_, err = bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	before := readAllFiles(t, session)
	failed := runTool(t, env, root, "", "finish", "--session-dir", session)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "session action busy")
	require.Equal(t, before, readAllFiles(t, session))
	require.NoFileExists(t, filepath.Join(session, "closed"))
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait())
	finished, report := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
	require.Equal(t, "passed", report["status"])
}

func TestFinishRetriesPartialCleanupWithoutRevalidation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission injection requires non-root")
	}
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "finish-partial-cleanup"))
	recordConsumer(t, env, session, agent, requestOne)
	runPreparedMock(t, env, session, "", requestOne)
	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	resource := str(descriptor["resourceRoot"])
	blocked := filepath.Join(resource, "blocked-cleanup")
	require.NoError(t, os.Mkdir(blocked, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(blocked, "remaining"), []byte("owned"), 0o600))
	require.NoError(t, os.Chmod(blocked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	failed, report := finishSession(t, env, session)
	require.NotZero(t, failed.code)
	require.Equal(t, false, report["cleanupComplete"])
	require.Equal(t, "passed", report["validationStatus"])
	require.FileExists(t, filepath.Join(resource, ".owner"))
	require.NoDirExists(t, filepath.Join(resource, "executions"))
	require.NoError(t, os.Chmod(blocked, 0o700))
	retried, final := finishSession(t, env, session)
	require.Zero(t, retried.code, retried.stderr)
	require.Equal(t, "passed", final["status"])
	require.Equal(t, report["executions"], final["executions"])
	require.NoDirExists(t, resource)
}

func TestFinishCleanupEvidenceWriteFailureCanRetry(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	cleanupPath := filepath.Join(session, "cleanup.json")
	require.NoError(t, os.Mkdir(cleanupPath, 0o700))
	failed, report := finishSession(t, env, session)
	require.NotZero(t, failed.code)
	require.Equal(t, "failed", report["status"])
	require.Equal(t, "abandoned", report["validationStatus"])
	require.Equal(t, true, report["cleanupComplete"])
	require.FileExists(t, filepath.Join(session, "closed"))
	require.Equal(t, report, readJSON(t, filepath.Join(session, "finish.json")))
	require.NoError(t, os.Remove(cleanupPath))
	retried, final := finishSession(t, env, session)
	require.Zero(t, retried.code, retried.stderr)
	require.Equal(t, "abandoned", final["status"])
	require.Equal(t, true, final["cleanupComplete"])
}

func TestFinishPersistsValidationBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	resource := str(readJSON(t, filepath.Join(session, "session.json"))["resourceRoot"])
	finishPath := filepath.Join(session, "finish.json")
	require.NoError(t, os.Mkdir(finishPath, 0o700))
	failed, report := finishSession(t, env, session)
	require.NotZero(t, failed.code)
	require.Equal(t, "failed", report["status"])
	require.Equal(t, false, report["cleanupComplete"])
	require.FileExists(t, filepath.Join(session, "closed"))
	require.DirExists(t, resource)
	require.NoError(t, os.Remove(finishPath))
	retried, final := finishSession(t, env, session)
	require.Zero(t, retried.code, retried.stderr)
	require.Equal(t, "abandoned", final["status"])
	require.NoDirExists(t, resource)
}
