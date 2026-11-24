package corpus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiCaptureCredentialModelAlias(t *testing.T) {
	write := func(t *testing.T, root, name, data string) {
		t.Helper()
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	}
	config := t.TempDir()
	write(t, config, "models.json", `{"providers":{"example":{"apiKey":"example","headers":{"Authorization":"private-token"}}}}`)
	const event = `{"direction":"s2c","wire":{"type":"message_end","message":{"role":"assistant","provider":"example"}}}`
	const history = `{"type":"model_change","provider":"example","modelId":"test-model"}`

	tests := []struct {
		name, path, content string
	}{
		{name: "routing metadata"},
		{name: "reply text", path: "raw/raw.jsonl", content: `{"direction":"s2c","wire":{"type":"message_end","message":{"role":"assistant","provider":"example","content":[{"type":"text","text":"example"}]}}}`},
		{name: "escaped metadata cannot mask reply text", path: "raw/raw.jsonl", content: `{"direction":"s2c","wire":{"type":"message_end","message":{"role":"assistant","provider":"ex\u0061mple","content":[{"type":"text","text":"example"}]}}}`},
		{name: "history text", path: "history/session.jsonl", content: `{"type":"message","message":{"role":"user","content":"example"}}`},
		{name: "invocation outside model flag", path: "raw/manifest.json", content: `{"native":{"args":["--mode","rpc","--system-prompt","example"]}}`},
		{name: "header credential", path: "history/session.jsonl", content: `{"type":"message","message":{"role":"assistant","provider":"private-token"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "raw/manifest.json", `{"native":{"args":["--model","example/test-model"]}}`)
			write(t, root, "raw/raw.jsonl", event)
			write(t, root, "history/session.jsonl", history)
			if test.path != "" {
				write(t, root, test.path, test.content)
			}
			scan, err := ScanPiCaptureCredentials(root, config, nil)
			if test.path == "" {
				require.NoError(t, err)
				require.Equal(t, "pass", scan)
			} else {
				require.ErrorIs(t, err, errCaptureCredentials)
				require.Equal(t, "quarantined", scan)
			}
		})
	}
}
