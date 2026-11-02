package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This real child process distinguishes EOF from a handled SIGINT. The ready
// frame is a barrier: signal handling and the stdin reader are installed first.
func recordEOFActionPeer(mode string) {
	var child *exec.Cmd
	var childDone chan error
	if mode == "direct-only" {
		child = exec.Command(os.Args[0], "-test.run=^TestRecordCLIHelper$", "--", "eof-action", "unhandled")
		child.Env = os.Environ()
		input, err := child.StdinPipe()
		if err != nil {
			os.Exit(90)
		}
		defer input.Close()
		output, err := child.StdoutPipe()
		if err != nil || child.Start() != nil {
			os.Exit(90)
		}
		if _, err = bufio.NewReader(output).ReadString('\n'); err != nil {
			os.Exit(90)
		}
		childDone = make(chan error, 1)
		go func() { childDone <- child.Wait() }()
	}
	signals := make(chan os.Signal, 1)
	if mode == "unhandled" {
		signal.Reset(syscall.SIGINT)
	} else if mode == "ignore" {
		signal.Ignore(syscall.SIGINT)
	} else {
		signal.Notify(signals, syscall.SIGINT)
	}
	eof := make(chan struct{})
	if mode == "closed-input" {
		_ = os.Stdin.Close()
	} else {
		go func() {
			r := bufio.NewReader(os.Stdin)
			for {
				line, err := r.ReadBytes('\n')
				if len(line) > 0 {
					_, _ = os.Stdout.Write(line)
				}
				if err != nil {
					close(eof)
					return
				}
			}
		}()
	}
	fmt.Fprintf(os.Stdout, "{\"ready\":%d}\n", os.Getpid())
	select {
	case <-eof:
		if mode == "ignore" {
			for {
				time.Sleep(time.Hour)
			}
		}
		fmt.Fprint(os.Stdout, "{\"closedBy\":\"EOF\"}\n")
		os.Exit(1)
	case <-signals:
		// Stdin must remain open throughout native graceful shutdown.
		select {
		case <-eof:
			os.Exit(92)
		case <-time.After(30 * time.Millisecond):
		}
		if child != nil {
			select {
			case <-childDone:
				os.Exit(91) // SIGINT must not reach the process group.
			default:
			}
			_ = child.Process.Kill()
			<-childDone
		}
		fmt.Fprint(os.Stdout, "{\"closedBy\":\"SIGINT\",\"data\":\""+strings.Repeat("x", 256*1024)+"\"}\n")
		fmt.Fprint(os.Stderr, "shutdown flushed\n")
		if mode == "nonzero" {
			os.Exit(17)
		}
	}
}

func TestRecordCLIStdinEOFAction(t *testing.T) {
	for _, tc := range []struct {
		name, action, mode    string
		proxyCode, nativeCode int
		why, signal           string
	}{
		{"default closes", "", "handled", 1, 1, "native-error", ""},
		{"explicit close", "close", "handled", 1, 1, "native-error", ""},
		{"interrupt drains", "interrupt", "handled", 0, 0, "EOF", ""},
		{"only direct child interrupted", "interrupt", "direct-only", 0, 0, "EOF", ""},
		{"interrupt nonzero fails", "interrupt", "nonzero", 1, 17, "native-error", ""},
		{"unhandled signal fails", "interrupt", "unhandled", 1, -1, "native-error", syscall.SIGINT.String()},
		{"ignored signal times out", "interrupt", "ignore", 3, -1, "deadline", syscall.SIGTERM.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := []string{"--timeout", "2"}
			if tc.action != "" {
				flags = append(flags, "--stdin-eof-action", tc.action)
			}
			p := startRecord(t, flags, "eof-action", tc.mode)
			r := bufio.NewReader(p.out)
			ready, err := r.ReadString('\n')
			require.NoError(t, err)
			var peer struct{ Ready int }
			require.NoError(t, json.Unmarshal([]byte(ready), &peer))
			require.Positive(t, peer.Ready)
			wire := " {\"raw\":1e+09,\"large\":9007199254740993,\"escaped\":\"\\u0061\"} \r\n"
			_, err = io.WriteString(p.in, wire)
			require.NoError(t, err)
			echo, err := r.ReadString('\n')
			require.NoError(t, err)
			assert.Equal(t, wire, echo)
			require.NoError(t, p.in.Close())
			output := recordReadAll(t, r)
			p.wait(t, tc.proxyCode)
			report, _ := p.reports(t)
			assert.Equal(t, tc.why, report.Reason)
			if tc.proxyCode == 0 {
				assert.Equal(t, "complete", report.CaptureStatus)
			} else {
				assert.Equal(t, "failed", report.CaptureStatus)
			}
			assert.True(t, report.Reaped)
			assert.Equal(t, tc.signal, report.NativeSignal)
			if tc.nativeCode >= 0 {
				require.NotNil(t, report.NativeExitCode)
				assert.Equal(t, tc.nativeCode, *report.NativeExitCode)
			} else {
				assert.Nil(t, report.NativeExitCode)
			}
			assert.ErrorIs(t, syscall.Kill(peer.Ready, 0), syscall.ESRCH)
			action := tc.action
			if action == "" {
				action = "close"
			}
			meta := readEOFReport(t, p.dir)
			assert.Equal(t, true, meta["clientEOF"])
			assert.Equal(t, action, meta["stdinEOFAction"])
			if action == "interrupt" {
				assert.Equal(t, "SIGINT", meta["stdinEOFSignal"])
			}
			if tc.mode == "handled" || tc.mode == "nonzero" || tc.mode == "direct-only" {
				assert.EqualValues(t, 4, report.Frames)
				if action == "interrupt" {
					assert.Equal(t, "{\"closedBy\":\"SIGINT\",\"data\":\""+strings.Repeat("x", 256*1024)+"\"}\n", string(output))
					stderr, err := os.ReadFile(filepath.Join(p.dir, "stderr.raw"))
					require.NoError(t, err)
					assert.Equal(t, "shutdown flushed\n", string(stderr))
				} else {
					assert.Equal(t, "{\"closedBy\":\"EOF\"}\n", string(output))
				}
			}
			raw, err := os.ReadFile(filepath.Join(p.dir, "raw.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, 2, strings.Count(string(raw), strings.TrimSuffix(wire, "\r\n")))
		})
	}
}

func TestRecordCLIStdinEOFFailuresDoNotInterrupt(t *testing.T) {
	for _, tc := range []struct{ name, mode, input string }{
		{"partial input", "handled", "{\"partial\":"},
		{"invalid frame", "handled", "{broken}\n"},
		{"failed forwarding", "closed-input", "{\"forward\":true}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := startRecord(t, []string{"--stdin-eof-action", "interrupt", "--timeout", "2"}, "eof-action", tc.mode)
			r := bufio.NewReader(p.out)
			_, err := r.ReadString('\n')
			require.NoError(t, err)
			_, err = io.WriteString(p.in, tc.input)
			require.NoError(t, err)
			require.NoError(t, p.in.Close())
			output := recordReadAll(t, r)
			p.wait(t, 1)
			report, _ := p.reports(t)
			assert.Equal(t, "failed", report.CaptureStatus)
			assert.Equal(t, "transport-error", report.Reason)
			assert.NotContains(t, string(output), "SIGINT")
			meta := readEOFReport(t, p.dir)
			assert.Equal(t, false, meta["clientEOF"])
			assert.Empty(t, meta["stdinEOFAction"])
			assert.Empty(t, meta["stdinEOFSignal"])
		})
	}
}

func TestRecordCLIStdinEOFNativeExitDoesNotInterrupt(t *testing.T) {
	p := startRecord(t, []string{"--stdin-eof-action", "interrupt"}, "emit", "{\"done\":true}\n")
	assert.Equal(t, "{\"done\":true}\n", string(recordReadAll(t, p.out)))
	p.wait(t, 0) // Caller input stays open; only native completion stops its reader.
	report, _ := p.reports(t)
	assert.Equal(t, "complete", report.CaptureStatus)
	meta := readEOFReport(t, p.dir)
	assert.Equal(t, false, meta["clientEOF"])
	assert.Empty(t, meta["stdinEOFAction"])
	assert.Empty(t, meta["stdinEOFSignal"])
}

func readEOFReport(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "transport.json"))
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}
