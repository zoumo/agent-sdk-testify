package agenttestify_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

const libraryRemoteReply = `{ "id":1, "result":{"synthetic":true,"large":9007199254740993,"exponent":1e+09} }`

func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "library-record-peer" {
		os.Exit(libraryRecordPeer(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func libraryRecordPeer(args []string) int {
	if len(args) == 0 {
		return 2
	}
	reader := bufio.NewReader(os.Stdin)
	if _, err := reader.ReadString('\n'); err != nil {
		return 2
	}
	switch args[0] {
	case "success":
		if _, err := fmt.Fprintln(os.Stdout, `{"id":1,"result":{"synthetic":true}}`); err != nil {
			return 3
		}
		if _, err := fmt.Fprintln(os.Stderr, "synthetic library record peer"); err != nil {
			return 3
		}
		_, _ = io.Copy(io.Discard, reader)
		return 0
	case "failure":
		if _, err := fmt.Fprintln(os.Stderr, "synthetic library record failure"); err != nil {
			return 3
		}
		return 7
	default:
		return 2
	}
}

func TestLibraryRecordWithoutCLI(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	sessionDir := filepath.Join(root, "session")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		SessionDir: sessionDir,
		Plan:       writeLibraryPlan(t, root),
	})
	require.NoError(t, err)
	require.Nil(t, prepared.Current)

	peer := writeLibraryRecordPeer(t, root, "success")
	result, stdout, stderr, recordErr := runLibraryRecord(t, prepared, agenttestify.RecordOptions{
		Command:    peer,
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		Timeout:    10 * time.Second,
	}, `{"id":1,"method":"synthetic"}`+"\n")
	require.NoError(t, recordErr)
	require.Empty(t, stderr)
	require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, strings.TrimSpace(stdout))
	require.Equal(t, cwd, workingDirectory(t))

	assertPublishedLibraryRecord(t, prepared, result)
	require.FileExists(t, result.ProcessReceiptPath)

	receipt := readLibraryJSON(t, result.ReceiptPath)
	require.Equal(t, "complete", receipt["captureStatus"])
	require.Equal(t, true, receipt["cleanupComplete"])
	require.Contains(t, string(readFile(t, filepath.Join(filepath.Dir(result.ReceiptPath), "stderr.raw"))), "synthetic library record peer")

	opened, err := agenttestify.Open(sessionDir)
	require.NoError(t, err)
	require.Equal(t, result.Material, opened.Current)
	require.Equal(t, cwd, workingDirectory(t))
}

func TestLibraryRecordFailureRetainsEvidenceAndCurrent(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	sessionDir := filepath.Join(root, "session")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		SessionDir: sessionDir,
		Plan:       writeLibraryPlan(t, root),
	})
	require.NoError(t, err)

	successPeer := writeLibraryRecordPeer(t, root, "success")
	success, _, _, err := runLibraryRecord(t, prepared, agenttestify.RecordOptions{
		Command:    successPeer,
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		Timeout:    10 * time.Second,
	}, `{"id":1,"method":"synthetic"}`+"\n")
	require.NoError(t, err)
	require.NotNil(t, success.Material)
	currentBefore := readFile(t, filepath.Join(sessionDir, "current.json"))

	failurePeer := writeLibraryRecordPeer(t, root, "failure")
	failed, stdout, stderr, recordErr := runLibraryRecord(t, prepared, agenttestify.RecordOptions{
		Command:    failurePeer,
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		Timeout:    10 * time.Second,
	}, `{"id":2,"method":"synthetic"}`+"\n")
	require.Error(t, recordErr)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.Equal(t, cwd, workingDirectory(t))
	require.Equal(t, currentBefore, readFile(t, filepath.Join(sessionDir, "current.json")))

	require.NotNil(t, failed)
	require.Equal(t, agenttestify.ConnectionStatusFailed, failed.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusFailed, failed.ExecutionStatus)
	require.True(t, failed.ExecutionComplete)
	require.Equal(t, prepared.SessionID, failed.SessionID)
	require.Equal(t, "main", failed.ConnectionID)
	require.NotEmpty(t, failed.ExecutionID)
	require.Nil(t, failed.Material)
	require.FileExists(t, failed.ExecutionPath)
	require.FileExists(t, failed.ReceiptPath)
	require.FileExists(t, failed.ProcessReceiptPath)
	require.FileExists(t, failed.ResultPath)
	require.Contains(t, string(readFile(t, filepath.Join(filepath.Dir(failed.ReceiptPath), "stderr.raw"))), "synthetic library record failure")

	resultReference := readLibraryJSON(t, failed.ResultPath)
	require.Equal(t, "failed", resultReference["status"])
	require.NotEmpty(t, resultReference["error"])
	failurePath := filepath.Join(filepath.Dir(failed.ExecutionPath), "receipts", "main.failure.json")
	require.FileExists(t, failurePath)
	require.Equal(t, "failed", readLibraryJSON(t, failurePath)["status"])

	opened, err := agenttestify.Open(sessionDir)
	require.NoError(t, err)
	require.Equal(t, success.Material, opened.Current)
	require.Equal(t, cwd, workingDirectory(t))
}

func TestLibraryRemoteRecordWithoutCLI(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	token := "synthetic-library-remote-token"
	tokenFile := filepath.Join(root, "private-bearer")
	require.NoError(t, os.WriteFile(tokenFile, []byte(token), 0o600))
	requests := make(chan string, 1)
	serverErrors := make(chan error, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			serverErrors <- fmt.Errorf("unexpected authorization header: %q", req.Header.Get("Authorization"))
			return
		}
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			serverErrors <- fmt.Errorf("accept websocket: %w", err)
			return
		}
		defer conn.CloseNow()
		_, payload, err := conn.Read(req.Context())
		if err != nil {
			serverErrors <- fmt.Errorf("read request: %w", err)
			return
		}
		requests <- string(payload)
		if err := conn.Write(req.Context(), websocket.MessageText, []byte(libraryRemoteReply)); err != nil {
			serverErrors <- fmt.Errorf("write response: %w", err)
			return
		}
		for {
			if _, _, err := conn.Read(req.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	sessionDir := filepath.Join(root, "session")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		SessionDir: sessionDir,
		Plan:       writeLibraryPlan(t, root),
	})
	require.NoError(t, err)
	require.Nil(t, prepared.Current)

	input, inputWriter, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = input.Close() })
	t.Cleanup(func() { _ = inputWriter.Close() })
	output, outputWriter, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close() })
	t.Cleanup(func() { _ = outputWriter.Close() })
	errOutput, err := os.CreateTemp(t.TempDir(), "stderr-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = errOutput.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	type outcome struct {
		result *agenttestify.RecordResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := prepared.Record(ctx, agenttestify.RecordOptions{
			URL:       "ws" + strings.TrimPrefix(server.URL, "http"),
			TokenFile: tokenFile,
			Timeout:   10 * time.Second,
		}, agenttestify.Streams{In: input, Out: outputWriter, Err: errOutput})
		done <- outcome{result: result, err: err}
	}()

	request := `{"id":1,"method":"synthetic"}` + "\n"
	_, err = inputWriter.WriteString(request)
	require.NoError(t, err)
	require.NoError(t, output.SetReadDeadline(time.Now().Add(10*time.Second)))
	response, err := bufio.NewReader(output).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, libraryRemoteReply+"\n", response) //nolint:testifylint // Exact raw JSON bytes are contractual.
	require.NoError(t, inputWriter.Close())

	var recorded outcome
	select {
	case recorded = <-done:
	case <-ctx.Done():
		t.Fatalf("remote record did not finish: %v", ctx.Err())
	}
	require.NoError(t, recorded.err)
	require.Equal(t, cwd, workingDirectory(t))
	material := assertPublishedLibraryRecord(t, prepared, recorded.result)
	require.Contains(t, string(readFile(t, filepath.Join(material.Path, "raw", "raw.jsonl"))), `"large":9007199254740993,"exponent":1e+09`)
	receipt := readLibraryJSON(t, recorded.result.ReceiptPath)
	require.Equal(t, "complete", receipt["captureStatus"])
	require.Equal(t, "remote-websocket", receipt["target"])
	require.Equal(t, true, receipt["remoteConnected"])
	require.Equal(t, true, receipt["remoteClosed"])
	require.Equal(t, false, receipt["nativeLaunched"])

	opened, err := agenttestify.Open(sessionDir)
	require.NoError(t, err)
	require.Equal(t, recorded.result.Material, opened.Current)

	select {
	case received := <-requests:
		require.Equal(t, strings.TrimSuffix(request, "\n"), received)
	case <-ctx.Done():
		t.Fatalf("remote service did not receive the request: %v", ctx.Err())
	}
	select {
	case serverErr := <-serverErrors:
		require.NoError(t, serverErr)
	default:
	}
}

func assertPublishedLibraryRecord(t *testing.T, prepared *agenttestify.Session, result *agenttestify.RecordResult) *agenttestify.Material {
	t.Helper()
	require.NotNil(t, result)
	require.Equal(t, agenttestify.ConnectionStatusPassed, result.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusPassed, result.ExecutionStatus)
	require.True(t, result.ExecutionComplete)
	require.Equal(t, prepared.SessionID, result.SessionID)
	require.Equal(t, "main", result.ConnectionID)
	require.NotEmpty(t, result.ExecutionID)
	require.NotNil(t, result.Material)
	require.Len(t, result.Material.PlanSHA256, 64)
	require.Len(t, result.Material.ManifestSHA256, 64)
	require.Len(t, result.Material.RecordingSHA256, 64)
	require.Equal(t, filepath.Join(prepared.SessionDir, "materials", result.Material.ID), result.Material.Path)
	require.FileExists(t, result.ExecutionPath)
	require.FileExists(t, result.ReceiptPath)
	require.FileExists(t, result.ResultPath)

	materialHashes := treeHashes(t, result.Material.Path)
	require.Equal(t, result.Material.PlanSHA256, materialHashes["plan.json"])
	require.Equal(t, result.Material.ManifestSHA256, materialHashes["manifest.json"])
	require.Equal(t, result.Material.RecordingSHA256, materialHashes[filepath.Join("raw", "raw.jsonl")])
	material := readLibraryJSON(t, filepath.Join(result.Material.Path, "material.json"))
	require.Equal(t, result.Material.ID, material["id"])
	require.Equal(t, result.Material.RecordingSHA256, material["recordingSHA256"])
	return result.Material
}

func runLibraryRecord(
	t *testing.T,
	session *agenttestify.Session,
	options agenttestify.RecordOptions,
	input string,
) (*agenttestify.RecordResult, string, string, error) {
	t.Helper()
	streams := newLibraryStreams(t, t.TempDir(), input)
	result, runErr := session.Record(t.Context(), options, streams.io)
	return result, streams.output(t), streams.errors(t), runErr
}

func writeLibraryRecordPeer(t *testing.T, root, mode string) string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\n"
	if race := os.Getenv("GORACE"); race != "" {
		script += "export GORACE=" + shellQuote(race) + "\n"
	}
	script += "exec " + shellQuote(self) + " library-record-peer " + shellQuote(mode) + " \"$@\"\n"
	path := filepath.Join(root, "synthetic-peer-"+mode)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
