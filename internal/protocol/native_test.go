package protocol

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeArgumentContract(t *testing.T) {
	for _, tt := range []struct {
		provider string
		args     []string
		valid    bool
	}{
		{"codex", nil, true},
		{"codex", []string{"app-server", "--listen=stdio://"}, true},
		{"codex", []string{"--listen=ws://localhost"}, false},
		{"codex", []string{"app-server", "--listen=stdio://", "app-server", "--listen=stdio://"}, false},
		{"claude", []string{"--input-format=json"}, false},
		{"claude", []string{"--input-format", "stream-json", "--output-format=stream-json", "--permission-prompt-tool", "stdio", "--verbose", "--include-partial-messages", "--model=x", "--resume=y"}, true},
		{"claude", []string{"--input-format", "stream-json", "--output-format=stream-json", "--input-format", "stream-json", "--output-format=stream-json"}, false},
		{"claude", nil, true},
		{"unknown", nil, false},
		{"codex", []string{"app-server", "--listen", "stdio://"}, true},
		{"claude", []string{"--output-format", "stream-json", "--input-format=stream-json", "--permission-prompt-tool", "stdio", "--verbose", "--include-partial-messages", "--model=x", "--resume=y"}, true},
		{"claude", []string{"--verbose", "--verbose"}, false},
		{"claude", []string{"--model", "x"}, false},
		{"claude", []string{"--output-format"}, false},
		{"claude", []string{"--dangerously-skip-permissions"}, false},
		{"codex", []string{"app-server=x"}, false},
	} {
		t.Run(fmt.Sprintf("%s/%v", tt.provider, tt.args), func(t *testing.T) {
			err := ValidateNativeArgs(tt.provider, tt.args)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestNativeArgumentRejectionDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		message string
	}{
		{"agent mismatch", []string{"--agent", "claude"}, "unsupported native startup argument"},
		{"native transport", []string{"app-server", "--listen", "http://localhost:1234"}, "unsupported native transport"},
		{"no matcher relaxation", []string{"--sdk-test-handshakes", "answer"}, "unsupported native startup argument"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.ErrorContains(t, ValidateNativeArgs("codex", tc.args), tc.message) })
	}
}
