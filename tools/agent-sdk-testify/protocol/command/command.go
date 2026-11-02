// Package command defines protocol CLI parsing, dispatch and exit codes.
package command

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

const Version = "0.1.0-dev"
const (
	ExitOK            = 0
	ExitFailure       = 1
	ExitConfiguration = 2
	ExitCanceled      = 3
	ExitDisconnect    = 4
	ExitUnimplemented = 5
)

type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string       { return e.Err.Error() }
func (e *Error) Unwrap() error       { return e.Err }
func Fail(code int, err error) error { return &Error{code, err} }
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitFailure
}

// IO supplies actual process descriptors so implementations can interrupt blocked
// reads/writes. A handler owns their use for one process invocation.
type IO struct{ In, Out, Err *os.File }
type RecordOptions struct {
	Output         string
	CWD            string
	Env            []string
	ClearEnv       bool
	Command        []string
	NativeVersion  string
	StdinEOFAction string
	Timeout        time.Duration
	ProtocolBytes  int64
	StderrBytes    int64
	MaxFrames      int64
	MaxFrameBytes  int64
	CleanupTimeout time.Duration
}
type MockOptions struct {
	Provider         string
	Scenario         string
	Receipt          string
	Timeout          time.Duration // zero means scenario timeout, then provider default
	Limits           recording.Limits
	Handshakes       string
	ProgressFD       int // -1 disables; >=3 is an inherited dedicated write descriptor
	RecordingVersion string
	NativeArgs       []string
}
type Handlers struct {
	Record  func(context.Context, RecordOptions, IO) error
	Mock    func(context.Context, MockOptions, IO) error
	Connect func(context.Context, ConnectOptions, IO) error
}

// New wires both capabilities once. Each implementation lives in its own owned
// entry file and receives fully validated CLI options. Runtime/data errors belong
// to the handler; argument errors always carry ExitConfiguration.
func New(h Handlers, streams IO) *cobra.Command {
	root := &cobra.Command{Use: "protocol", Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "agent-sdk-testify protocol"}, Short: "Record stdio JSONL or replay an offline native protocol", Version: Version, SilenceErrors: true, SilenceUsage: true}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	root.SetVersionTemplate("agent-sdk-testify {{.Version}}\n")
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return Fail(ExitConfiguration, err) })
	root.AddCommand(recordCommand(h.Record, streams), mockCommand(h.Mock, streams), connectCommand(h.Connect, streams))
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print the executable version", Args: cobra.NoArgs, Run: func(c *cobra.Command, _ []string) { c.Print("agent-sdk-testify " + Version + "\n") }})
	return root
}

// Execute classifies dispatch/argument errors without mistaking runtime failures
// for configuration errors. Handlers should return Fail for non-default codes.
func Execute(ctx context.Context, args []string, h Handlers, streams IO) int {
	root := New(h, streams)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var classified *Error
	if !errors.As(err, &classified) {
		err = Fail(ExitConfiguration, err)
	}
	fmt.Fprintln(streams.Err, "agent-sdk-testify:", err)
	return ExitCode(err)
}

func recordCommand(handler func(context.Context, RecordOptions, IO) error, streams IO) *cobra.Command {
	var o RecordOptions
	var seconds, cleanup float64
	c := &cobra.Command{Use: "record --output DIR [flags] -- EXECUTABLE [ARG ...]", Short: "Capture a child process over stdio JSONL", Args: cobra.ArbitraryArgs}
	f := c.Flags()
	f.StringVar(&o.Output, "output", "", "New output directory (must not exist)")
	f.StringVar(&o.CWD, "cwd", "", "Child working directory (default: current directory)")
	f.StringArrayVar(&o.Env, "env", nil, "Child environment override KEY=VALUE (repeatable)")
	f.BoolVar(&o.ClearEnv, "clear-env", false, "Start child with an empty environment")
	f.StringVar(&o.NativeVersion, "native-version", "", "Caller-declared executable version (never probed)")
	f.StringVar(&o.StdinEOFAction, "stdin-eof-action", "close", "On clean client EOF: close native stdin or interrupt the direct child with SIGINT")
	f.Float64Var(&seconds, "timeout", 0, "Capture timeout in seconds; 0 disables")
	f.Int64Var(&o.ProtocolBytes, "protocol-bytes", 0, "Total protocol byte limit; 0 disables")
	f.Int64Var(&o.StderrBytes, "stderr-bytes", 0, "Native stderr byte limit; 0 disables")
	f.Int64Var(&o.MaxFrames, "max-frames", 0, "Total capture frame limit; 0 disables")
	f.Int64Var(&o.MaxFrameBytes, "max-frame-bytes", 0, "Maximum native line bytes; 0 disables")
	f.Float64Var(&cleanup, "cleanup-timeout", 2, "Graceful termination seconds before forced cleanup")
	c.RunE = func(c *cobra.Command, args []string) error {
		bad := func(message string) error { return Fail(ExitConfiguration, errors.New(message)) }
		if o.Output == "" || len(args) == 0 || c.ArgsLenAtDash() != 0 || args[0] == "" {
			return bad("record requires --output DIR -- EXECUTABLE [ARG ...]")
		}
		var err error
		o.Timeout, err = duration(seconds, true)
		if err != nil {
			return bad("invalid record timeout")
		}
		o.CleanupTimeout, err = duration(cleanup, false)
		if err != nil {
			return bad("invalid cleanup timeout")
		}
		if o.ProtocolBytes < 0 || o.StderrBytes < 0 || o.MaxFrames < 0 || o.MaxFrameBytes < 0 {
			return bad("capture limits must be nonnegative")
		}
		if o.StdinEOFAction != "close" && o.StdinEOFAction != "interrupt" {
			return bad("stdin EOF action must be close or interrupt")
		}
		seen := map[string]bool{}
		for _, env := range o.Env {
			key, value, ok := strings.Cut(env, "=")
			if !ok || key == "" || strings.ContainsRune(key+value, 0) || seen[key] {
				return bad("environment overrides require unique nonempty KEY=VALUE")
			}
			seen[key] = true
		}
		o.Command = append([]string(nil), args...)
		if handler == nil {
			return Fail(ExitUnimplemented, errors.New("record is not implemented"))
		}
		return runtimeError(handler(c.Context(), o, streams))
	}
	return c
}

func mockCommand(handler func(context.Context, MockOptions, IO) error, streams IO) *cobra.Command {
	var o MockOptions
	var seconds float64
	c := &cobra.Command{Use: "mock --provider codex|claude --scenario FILE [flags] [native startup arguments]", Short: "Replay an explicit recording as a strict offline peer", DisableFlagParsing: true}
	f := c.Flags()
	f.StringVar(&o.Provider, "provider", "", "Native protocol adapter: codex or claude")
	f.StringVar(&o.Scenario, "scenario", "", "Explicit JSONL or v1 scenario file")
	f.StringVar(&o.Receipt, "receipt", "", "New success receipt file")
	f.Float64Var(&seconds, "timeout", 0, "Positive seconds; default: scenario timeout, then 30 codex / 10 claude")
	f.IntVar(&o.Limits.MaxFrames, "max-frames", 10000, "Maximum frames (1..10000)")
	f.Int64Var(&o.Limits.MaxBytes, "max-bytes", 0, "Source and per-direction traffic byte limit (default: 8 MiB codex / 16 MiB claude)")
	f.Int64Var(&o.Limits.MaxFrameBytes, "max-frame-bytes", 2<<20, "Exclusive native payload byte bound")
	f.StringVar(&o.Handshakes, "sdk-test-handshakes", "", "Explicit Codex test control: answer or answer-and-assert")
	f.IntVar(&o.ProgressFD, "progress-fd", -1, "Inherited consumption notification descriptor (>=3)")
	f.StringVar(&o.RecordingVersion, "recording-version", "", "Assert present native claude_code_version fields; no version whitelist")
	f.Bool("help", false, "Help for mock")
	c.RunE = func(c *cobra.Command, args []string) error {
		bad := func(err error) error { return Fail(ExitConfiguration, err) }
		var flags, native []string
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
			flag := f.Lookup(name)
			if !strings.HasPrefix(arg, "--") || flag == nil {
				native = append(native, arg)
				continue
			}
			flags = append(flags, arg)
			if !hasValue && flag.NoOptDefVal == "" {
				if i+1 >= len(args) {
					return bad(fmt.Errorf("missing value for --%s", name))
				}
				i++
				flags = append(flags, args[i])
			}
		}
		if err := f.Parse(flags); err != nil {
			return bad(err)
		}
		if help, _ := f.GetBool("help"); help {
			return c.Help()
		}
		if o.Scenario == "" {
			return bad(errors.New("--scenario is required"))
		}
		if o.Limits.MaxFrames < 1 || (f.Changed("max-bytes") && o.Limits.MaxBytes <= 0) || o.Limits.MaxFrameBytes <= 0 {
			return bad(errors.New("mock limits must be positive"))
		}
		var err error
		o.Limits, err = o.Limits.Resolve(o.Provider)
		if err != nil {
			return bad(err)
		}
		if f.Changed("timeout") {
			o.Timeout, err = duration(seconds, false)
			if err != nil {
				return bad(err)
			}
		}
		if o.Handshakes != "" && (o.Provider != "codex" || (o.Handshakes != "answer" && o.Handshakes != "answer-and-assert")) {
			return bad(errors.New("invalid Codex SDK test handshake mode"))
		}
		if o.ProgressFD < -1 || (o.ProgressFD >= 0 && o.ProgressFD < 3) {
			return bad(errors.New("progress descriptor must be >=3"))
		}
		if err = ValidateNativeArgs(o.Provider, native); err != nil {
			return bad(err)
		}
		o.NativeArgs = native
		if handler == nil {
			return Fail(ExitUnimplemented, errors.New("mock is not implemented"))
		}
		return runtimeError(handler(c.Context(), o, streams))
	}
	return c
}

func runtimeError(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return Fail(ExitFailure, err)
}
func duration(seconds float64, zero bool) (time.Duration, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || (!zero && seconds == 0) || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, errors.New("timeout seconds out of range")
	}
	d := time.Duration(seconds * float64(time.Second))
	if seconds > 0 && d == 0 {
		return 0, errors.New("timeout below one nanosecond")
	}
	return d, nil
}

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
