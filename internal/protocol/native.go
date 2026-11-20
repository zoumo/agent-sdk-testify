package protocol

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
)

// ValidateNativeArgs checks the reviewed stdio startup spellings for official
// recording and legacy mock declarations.
func ValidateNativeArgs(provider string, args []string) error {
	if _, err := recording.DefaultLimits(provider); err != nil {
		return err
	}
	spec, ok := nativeArgumentSpecs[provider]
	if !ok {
		return fmt.Errorf("unsupported native provider %q", provider)
	}
	return validateNativeArguments(spec, args)
}

// ValidateReplayPeerArgs rejects meta commands and incompatible transport
// declarations. Replay otherwise ignores the SDK startup arguments.
func ValidateReplayPeerArgs(provider string, args []string) error {
	stdio := nativeArgumentSpecs[provider].values
	for i := 0; i < len(args); i++ {
		name, value, equals := strings.Cut(args[i], "=")
		switch name {
		case "--help", "-h", "--version", "-v", "-V":
			return errors.New("native meta commands are unsupported by mock")
		case "help", "version":
			if i == 0 {
				return errors.New("native meta commands are unsupported by mock")
			}
		}
		want, ok := stdio[name]
		if !ok {
			continue
		}
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
	}
	return nil
}

type nativeArgumentSpec struct {
	bare         map[string]bool
	flags        map[string]bool
	values       map[string]string
	equalsValues map[string]bool
}

var nativeArgumentSpecs = map[string]nativeArgumentSpec{
	"codex":  codexNativeArguments,
	"claude": claudeNativeArguments,
}

func validateNativeArguments(spec nativeArgumentSpec, args []string) error {
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name, value, equals := strings.Cut(args[i], "=")
		if seen[name] {
			return fmt.Errorf("duplicate native startup argument %q", name)
		}
		seen[name] = true
		if spec.bare[name] && !equals {
			continue
		}
		if spec.equalsValues[name] && equals && value != "" {
			continue
		}
		if want, ok := spec.values[name]; ok {
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
		} else if !spec.flags[name] || equals {
			return fmt.Errorf("unsupported native startup argument %q", name)
		}
	}
	return nil
}
