package protocol

import (
	"fmt"
	"strings"

	"github.com/zoumo/agent-sdk-testify/protocol/recording"
)

// ValidateNativeArgs accepts only the reviewed stdio startup spellings used by
// existing SDK adapters. They are compatibility declarations, never executed.
func ValidateNativeArgs(provider string, args []string) error {
	if _, err := recording.DefaultLimits(provider); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name, value, equals := strings.Cut(args[i], "=")
		if seen[name] {
			return fmt.Errorf("duplicate native startup argument %q", name)
		}
		seen[name] = true
		if provider == "codex" && name == "app-server" && !equals {
			continue
		}
		values := map[string]string{"--listen": "stdio://"}
		flags := map[string]bool{}
		if provider == "claude" {
			values = map[string]string{"--output-format": "stream-json", "--input-format": "stream-json", "--permission-prompt-tool": "stdio"}
			flags = map[string]bool{"--verbose": true, "--include-partial-messages": true}
			if (name == "--model" || name == "--resume") && equals && value != "" {
				continue
			}
		}
		if want, ok := values[name]; ok {
			if !equals {
				i++
				if i == len(args) {
					return fmt.Errorf("missing native startup value for %s", name)
				}
				value = args[i]
			}
			if value != want {
				return fmt.Errorf("unsupported native transport/format for %s", name)
			}
		} else if !flags[name] || equals {
			return fmt.Errorf("unsupported native startup argument %q", name)
		}
	}
	return nil
}
