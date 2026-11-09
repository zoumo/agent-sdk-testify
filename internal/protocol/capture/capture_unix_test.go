//go:build darwin || linux

package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFailureRetainsStderrWhenForwardingFails(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "stdin-closed")
	script := `trap '' TERM
exec 0<&-
printf ready > "$1"
sleep 1
printf native-failure >&2
exit 7
`
	output := filepath.Join(root, "capture")
	input, inputWriter, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = inputWriter.Close() })
	clientOut, err := os.Create(filepath.Join(root, "client-out"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientOut.Close() })
	clientErr, err := os.Create(filepath.Join(root, "client-err"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientErr.Close() })

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			Output:         output,
			CWD:            root,
			Env:            []string{"PATH=" + os.Getenv("PATH")},
			ClearEnv:       true,
			Command:        []string{"/bin/sh", "-c", script, "capture-test", marker},
			NativeVersion:  "test-native",
			ProxyVersion:   "test-proxy",
			StdinEOFAction: "close",
			Timeout:        5 * time.Second,
			CleanupTimeout: 2 * time.Second,
		}, IO{In: input, Out: clientOut, Err: clientErr})
	}()

	require.Eventually(t, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)
	_, err = inputWriter.WriteString(`{"id":1}` + "\n")
	require.NoError(t, err)
	require.NoError(t, inputWriter.Close())

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not return")
	}
	stderr, err := os.ReadFile(filepath.Join(output, "stderr.raw"))
	require.NoError(t, err)
	require.Contains(t, string(stderr), "native-failure")
}
