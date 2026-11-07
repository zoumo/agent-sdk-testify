package command

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zoumo/agent-sdk-testify/internal/application"
)

func newMockCommand(streams IO) *cobra.Command {
	var o application.MockOptions
	var seconds float64
	var help bool
	o.ProgressFD = -1
	c := &cobra.Command{
		Use:                "mock [flags] [native startup arguments]",
		Short:              "Replay recorded native protocol",
		Long:               "Replay only the current recording in a prepared session. Native startup arguments declare protocol compatibility; Agent meta-commands are unsupported.",
		DisableFlagParsing: true,
	}
	f := c.Flags()
	f.StringVar(&o.SessionDir, "session-dir", "", "Prepared session directory")
	f.StringVar(&o.Connection, "connection", "main", "Declared connection ID")
	f.Float64Var(&seconds, "timeout", 0, "Seconds; zero uses material/session defaults")
	f.IntVar(&o.ResultFD, "result-fd", -1, "Inherited execution result notification descriptor (>=3)")
	f.IntVar(&o.ProgressFD, "progress-fd", -1, "Inherited full-consumption notification descriptor (>=3)")
	f.BoolVarP(&help, "help", "h", false, "Help for mock")
	c.RunE = func(c *cobra.Command, args []string) error {
		native, help, err := parseInterleavedFlags(f, args)
		if err != nil {
			return err
		}
		if help {
			return c.Help()
		}
		o.Timeout, err = secondsDuration(seconds)
		if err != nil {
			return err
		}
		o.NativeArgs = native
		_, err = application.Mock(c.Context(), o, application.Streams{In: streams.In, Out: streams.Out, Err: streams.Err})
		return err
	}
	return c
}

// parseInterleavedFlags separates reviewed command flags from native startup
// arguments. Unknown flags remain byte-for-byte native input.
func parseInterleavedFlags(flags *pflag.FlagSet, args []string) ([]string, bool, error) {
	var parsed, native []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			native = append(native, args[i+1:]...)
			break
		}
		if arg == "-h" {
			arg = "--help"
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		flag := flags.Lookup(name)
		if !strings.HasPrefix(arg, "--") || flag == nil {
			native = append(native, arg)
			continue
		}
		parsed = append(parsed, arg)
		if !hasValue && flag.NoOptDefVal == "" {
			if i+1 == len(args) {
				return nil, false, fmt.Errorf("missing value for --%s", name)
			}
			i++
			parsed = append(parsed, args[i])
		}
	}
	if err := flags.Parse(parsed); err != nil {
		return nil, false, err
	}
	help, err := flags.GetBool("help")
	return native, help, err
}

func secondsDuration(seconds float64) (time.Duration, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, errors.New("invalid mock timeout")
	}
	duration := time.Duration(seconds * float64(time.Second))
	if seconds > 0 && duration == 0 {
		return 0, errors.New("mock timeout must be at least one nanosecond")
	}
	return duration, nil
}
