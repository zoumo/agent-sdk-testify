package command

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

func newInternalProtocolCommand(streams IO) *cobra.Command {
	root := &cobra.Command{
		Use:           "internal-protocol",
		Short:         "Record stdio JSONL or replay an offline native protocol",
		Version:       version.Version,
		Hidden:        true,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unknown command %q", args[0])
			}
			return c.Help()
		},
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	root.SetVersionTemplate("agent-sdk-testify {{.Version}}\n")
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		protocolRecordCommand(streams),
		protocolMockCommand(streams),
		protocolConnectCommand(streams),
		versionCommand(),
	)
	return root
}

func protocolRecordCommand(streams IO) *cobra.Command {
	var o protocol.RecordOptions
	var seconds, cleanup float64
	c := &cobra.Command{
		Use:   "record --output DIR [flags] -- EXECUTABLE [ARG ...]",
		Short: "Capture a child process over stdio JSONL",
		Args:  cobra.ArbitraryArgs,
	}
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
		if o.Output == "" || len(args) == 0 || c.ArgsLenAtDash() != 0 || args[0] == "" {
			return errors.New("record requires --output DIR -- EXECUTABLE [ARG ...]")
		}
		var err error
		o.Timeout, err = duration(seconds, true)
		if err != nil {
			return errors.New("invalid record timeout")
		}
		o.CleanupTimeout, err = duration(cleanup, false)
		if err != nil {
			return errors.New("invalid cleanup timeout")
		}
		if o.ProtocolBytes < 0 || o.StderrBytes < 0 || o.MaxFrames < 0 || o.MaxFrameBytes < 0 {
			return errors.New("capture limits must be nonnegative")
		}
		if o.StdinEOFAction != "close" && o.StdinEOFAction != "interrupt" {
			return errors.New("stdin EOF action must be close or interrupt")
		}
		seen := map[string]bool{}
		for _, env := range o.Env {
			key, value, ok := strings.Cut(env, "=")
			if !ok || key == "" || strings.ContainsRune(key+value, 0) || seen[key] {
				return errors.New("environment overrides require unique nonempty KEY=VALUE")
			}
			seen[key] = true
		}
		o.Command = append([]string(nil), args...)
		return protocol.Record(c.Context(), o, protocol.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
	}
	return c
}

func protocolMockCommand(streams IO) *cobra.Command {
	var o protocol.MockOptions
	var seconds float64
	var help bool
	c := &cobra.Command{
		Use:                "mock --provider codex|claude --scenario FILE [flags] [native startup arguments]",
		Short:              "Replay an explicit recording as a strict offline peer",
		DisableFlagParsing: true,
	}
	o.ProgressFD = -1
	f := c.Flags()
	f.StringVar(&o.Provider, "provider", "", "Native protocol adapter: codex or claude")
	f.StringVar(&o.Scenario, "scenario", "", "Explicit JSONL or v1 scenario file")
	f.StringVar(&o.Receipt, "receipt", "", "New success receipt file")
	f.Float64Var(&seconds, "timeout", 0, "Positive seconds; default: scenario timeout, then 30 codex / 10 claude")
	f.IntVar(&o.Limits.MaxFrames, "max-frames", 10000, "Maximum frames (1..10000)")
	f.Int64Var(&o.Limits.MaxBytes, "max-bytes", 0, "Source and per-direction traffic byte limit")
	f.Int64Var(&o.Limits.MaxFrameBytes, "max-frame-bytes", 2<<20, "Exclusive native payload byte bound")
	f.StringVar(&o.Handshakes, "sdk-test-handshakes", "", "Explicit Codex test control: answer or answer-and-assert")
	f.IntVar(&o.ProgressFD, "progress-fd", -1, "Inherited consumption notification descriptor (>=3)")
	f.StringVar(&o.RecordingVersion, "recording-version", "", "Assert present native claude_code_version fields")
	f.BoolVarP(&help, "help", "h", false, "Help for mock")
	c.RunE = func(c *cobra.Command, args []string) error {
		native, requestedHelp, err := parseInterleavedFlags(f, args)
		if err != nil {
			return err
		}
		if requestedHelp || help {
			return c.Help()
		}
		if o.Scenario == "" {
			return errors.New("--scenario is required")
		}
		if o.Limits.MaxFrames < 1 || (f.Changed("max-bytes") && o.Limits.MaxBytes <= 0) || o.Limits.MaxFrameBytes <= 0 {
			return errors.New("mock limits must be positive")
		}
		o.Limits, err = o.Limits.Resolve(o.Provider)
		if err != nil {
			return err
		}
		if f.Changed("timeout") {
			o.Timeout, err = duration(seconds, false)
			if err != nil {
				return err
			}
		}
		if o.Handshakes != "" && (o.Provider != "codex" || (o.Handshakes != "answer" && o.Handshakes != "answer-and-assert")) {
			return errors.New("invalid Codex SDK test handshake mode")
		}
		if o.ProgressFD < -1 || (o.ProgressFD >= 0 && o.ProgressFD < 3) {
			return errors.New("progress descriptor must be >=3")
		}
		if err := protocol.ValidateNativeArgs(o.Provider, native); err != nil {
			return err
		}
		o.NativeArgs = native
		return protocol.Mock(c.Context(), o, protocol.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
	}
	return c
}

func protocolConnectCommand(streams IO) *cobra.Command {
	var o protocol.ConnectOptions
	c := &cobra.Command{
		Use:   "connect --url URL --token-file PATH",
		Short: "Bridge stdio JSONL to one existing authenticated WebSocket connection",
		Args:  cobra.NoArgs,
	}
	c.Flags().StringVar(&o.URL, "url", "", "Explicit ws/wss endpoint")
	c.Flags().StringVar(&o.TokenFile, "token-file", "", "Private bearer-token file")
	c.Flags().Int64Var(&o.MaxFrameBytes, "max-frame-bytes", 4<<20, "Maximum JSON message bytes")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if o.URL == "" || o.TokenFile == "" || o.MaxFrameBytes < 1 || o.MaxFrameBytes > 1<<30 {
			return errors.New("connect requires an endpoint, token file and positive bounded frame size")
		}
		return protocol.Connect(c.Context(), o, protocol.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
	}
	return c
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
