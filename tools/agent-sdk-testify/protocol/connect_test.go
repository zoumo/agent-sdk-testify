package protocol

import (
	"bufio"
	"context"
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
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/command"
)

func TestConnectPreservesJSONAndClosesOnlyConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			kind, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), kind, data); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		input, writer, err := os.Pipe()
		require.NoError(t, err)
		output, sink, err := os.Pipe()
		require.NoError(t, err)
		token := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(token, []byte("synthetic-token\n"), 0600))
		done := make(chan error, 1)
		go func() {
			done <- runConnect(ctx, command.ConnectOptions{URL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenFile: token, MaxFrameBytes: 1 << 20}, command.IO{In: input, Out: sink, Err: os.Stderr})
		}()
		const line = "{\"unknown\":9007199254740993,\"number\":1.00,\"null\":null}\n"
		_, err = io.WriteString(writer, line)
		require.NoError(t, err)
		got, err := bufio.NewReader(output).ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, line, got)
		require.NoError(t, writer.Close())
		require.NoError(t, <-done)
		cancel()
		_ = input.Close()
		_ = output.Close()
		_ = sink.Close()
	}
}

func TestConnectRejectsPartialJSONAndRemoteEarlyClose(t *testing.T) {
	for _, partial := range []bool{true, false} {
		t.Run(map[bool]string{true: "partial-input", false: "early-server-close"}[partial], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.CloseNow()
				if !partial {
					_ = c.Close(websocket.StatusNormalClosure, "server ended")
					return
				}
				_, _, _ = c.Read(r.Context())
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			input, writer, err := os.Pipe()
			require.NoError(t, err)
			defer input.Close()
			defer writer.Close()
			output, sink, err := os.Pipe()
			require.NoError(t, err)
			defer output.Close()
			defer sink.Close()
			token := filepath.Join(t.TempDir(), "token")
			require.NoError(t, os.WriteFile(token, []byte("synthetic-token"), 0600))
			done := make(chan error, 1)
			go func() {
				done <- runConnect(ctx, command.ConnectOptions{URL: "ws" + strings.TrimPrefix(server.URL, "http"), TokenFile: token, MaxFrameBytes: 1 << 20}, command.IO{In: input, Out: sink, Err: os.Stderr})
			}()
			if partial {
				_, err := io.WriteString(writer, `{"partial":true}`)
				require.NoError(t, err)
				require.NoError(t, writer.Close())
			}
			require.Error(t, <-done)
		})
	}
}
