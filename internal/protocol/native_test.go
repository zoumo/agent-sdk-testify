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
