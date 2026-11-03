package command

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
	"github.com/zoumo/agent-sdk-testify/internal/mock"
)

func newMockCommand() *cobra.Command {
	var o mock.Options
	var seconds float64
	var help bool
	o.ProgressFD = -1
	c := &cobra.Command{
		Use:                "mock [flags] [native startup arguments]",
		Short:              "Replay recorded native protocol",
		Long:               "Replay a capture or expose a prepared session. Builtin queries use mock --list/--describe. The managed mock alias prepares, runs and finishes one transient session.",
		DisableFlagParsing: true,
	}
	f := c.Flags()
	f.BoolVar(&o.List, "list", false, "List full builtin coverage")
	f.BoolVar(&o.Describe, "describe", false, "Describe one builtin or external material")
	f.BoolVar(&o.JSON, "json", false, "JSON output (schemaVersion 1)")
	f.StringVar(&o.Agent, "agent", "", "Agent type filter or selector")
	f.StringVar(&o.Case, "case", "", "Builtin case ID")
	f.StringVar(&o.Recording, "recording", "", "Absolute capture directory")
	f.StringVar(&o.Report, "report", "", "New persistent receipt file")
	f.StringVar(&o.SessionDir, "session-dir", "", "Prepared session directory")
	f.StringVar(&o.Connection, "connection", "main", "Declared connection ID")
	f.Float64Var(&seconds, "timeout", 0, "Seconds; zero uses material/session defaults")
	f.IntVar(&o.ProgressFD, "progress-fd", -1, "Inherited full-consumption notification descriptor (>=3)")
	f.BoolVarP(&help, "help", "h", false, "Help for mock")
	c.RunE = func(c *cobra.Command, args []string) error {
		if err := rejectRemovedMockModes(args); err != nil {
			return exitcode.Fail(exitcode.Configuration, err)
		}
		native, help, err := parseInterleavedFlags(f, args)
		if err != nil {
			return exitcode.Fail(exitcode.Configuration, err)
		}
		if help {
			return c.Help()
		}
		o.Timeout, err = secondsDuration(seconds)
		if err != nil {
			return exitcode.Fail(exitcode.Configuration, err)
		}
		o.NativeArgs = native
		if err := validateMockModes(o); err != nil {
			return exitcode.Fail(exitcode.Configuration, err)
		}
		return runtimeError(mock.Run(c.Context(), o))
	}
	return c
}

func rejectRemovedMockModes(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			return nil
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		switch name {
		case "prepare", "run", "finish", "output":
			return fmt.Errorf("mock --%s is unavailable; use prepare, mock --session-dir, and finish", name)
		case "provider":
			return errors.New("--provider is unavailable; use --agent")
		}
	}
	return nil
}

func validateMockModes(o mock.Options) error {
	if (o.List || o.Describe) && (o.SessionDir != "" || len(o.NativeArgs) != 0) {
		return errors.New("builtin query cannot be combined with a session")
	}
	if o.SessionDir != "" && o.Case != "" {
		return errors.New("mock modes are mutually exclusive")
	}
	if o.JSON && !o.List && !o.Describe {
		return errors.New("--json is only valid with --list or --describe")
	}
	if o.SessionDir != "" && o.Recording != "" && !filepath.IsAbs(o.Recording) {
		return errors.New("--recording must be an absolute path")
	}
	return nil
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
