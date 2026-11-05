// Package command owns the single Cobra command tree, process I/O and error
// reporting. Business packages receive typed options and never parse argv.
package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/record"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type IO struct{ In, Out, Err *os.File }

func Execute(ctx context.Context, args []string, streams IO) int {
	root := New(streams)
	root.SetArgs(internalArgs(args))
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	fmt.Fprintln(streams.Err, "agent-sdk-testify:", err)
	return 1
}

func New(streams IO) *cobra.Command {
	root := &cobra.Command{
		Use:           "agent-sdk-testify",
		Short:         "Prepare, record, verify, mock and export SDK protocol sessions",
		Version:       version.Version,
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
		prepareCommand(),
		exportCommand(),
		versionCommand(),
		peerCommand(streams),
	)
	agent := &cobra.Command{Use: "agent", Short: "Native Agent protocol peers", Args: cobra.NoArgs}
	agent.AddCommand(recordCommand(streams), mockCommand())
	root.AddCommand(agent)
	officialCommands := &cobra.Command{Use: "official", Short: "Official SDK orchestration", Args: cobra.NoArgs}
	officialCommands.AddCommand(officialRecordCommand(), verifyCommand())
	root.AddCommand(officialCommands)
	registerGenerate(root, streams)
	return root
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the executable version",
		Args:  cobra.NoArgs,
		Run: func(c *cobra.Command, _ []string) {
			c.Print("agent-sdk-testify " + version.Version + "\n")
		},
	}
}

func exportCommand() *cobra.Command {
	var sessionDir, output string
	c := &cobra.Command{
		Use:   "export [flags]",
		Short: "Export the complete current recording",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if !filepath.IsAbs(sessionDir) || !filepath.IsAbs(output) {
				return errors.New("export requires absolute --session-dir and --output paths")
			}
			return lifecycle.Export(sessionDir, output)
		},
	}
	c.Flags().StringVar(&sessionDir, "session-dir", "", "prepared session directory")
	c.Flags().StringVar(&output, "output", "", "new recording directory outside session")
	return c
}

func prepareCommand() *cobra.Command {
	var o lifecycle.PrepareOptions
	c := &cobra.Command{
		Use:   "prepare [flags]",
		Short: "Create a prepared test session",
		Args:  cobra.NoArgs,
	}
	c.Flags().StringVar(&o.SessionDir, "session-dir", "", "new persistent session directory")
	c.Flags().StringVar(&o.Agent, "agent", "", "agent type: codex or claude")
	c.Flags().StringVar(&o.Case, "case", "", "official case ID")
	c.Flags().StringVar(&o.Plan, "plan", "", "consumer plan file")
	c.Flags().StringVar(&o.Recording, "recording", "", "existing recording directory")
	c.Flags().StringVar(&o.Model, "model", "", "native model")
	c.Flags().StringVar(&o.InitialModel, "initial-model", "", "initial model for a model-change case")
	c.Flags().StringVar(&o.HistorySeed, "history-seed", "", "saved native history seed directory")
	c.Flags().Int64Var(&o.ProtocolBytes, "protocol-bytes", 0, "total protocol byte limit; zero uses material defaults")
	c.Flags().Int64Var(&o.StderrBytes, "stderr-bytes", 0, "native stderr byte limit; zero uses material defaults")
	c.Flags().Int64Var(&o.MaxFrames, "max-frames", 0, "frame limit; zero uses material defaults")
	c.Flags().Int64Var(&o.MaxFrameBytes, "max-frame-bytes", 0, "maximum native frame bytes; zero uses material defaults")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if o.SessionDir == "" {
			return errors.New("prepare requires --session-dir")
		}
		if !filepath.IsAbs(o.SessionDir) {
			return errors.New("--session-dir must be an absolute path")
		}
		if o.ProtocolBytes < 0 || o.StderrBytes < 0 || o.MaxFrames < 0 || o.MaxFrameBytes < 0 {
			return errors.New("prepare limits must be nonnegative")
		}
		if err := requireNewSessionDir(o.SessionDir); err != nil {
			return err
		}
		_, err := lifecycle.Prepare(o)
		return err
	}
	return c
}

func recordCommand(streams IO) *cobra.Command {
	o := record.Options{ResultFD: -1}
	c := &cobra.Command{
		Use:   "record [flags] [-- ARG ...]",
		Short: "Record a native Agent interaction in a prepared session",
		Args:  cobra.ArbitraryArgs,
	}
	c.Flags().StringVar(&o.SessionDir, "session-dir", "", "prepared session directory")
	c.Flags().StringVar(&o.Command, "cmd", "", "absolute native Agent executable")
	c.Flags().StringVar(&o.Connection, "connection", "main", "declared record connection")
	c.Flags().DurationVar(&o.ConnectTimeout, "connect-timeout", 0, "retry unavailable remote connections for this duration")
	c.Flags().StringVar(&o.URL, "url", "", "existing authenticated ws/wss endpoint (excludes --cmd and native arguments)")
	c.Flags().StringVar(&o.TokenFile, "token-file", "", "private bearer-token file for --url (never persisted)")
	c.Flags().DurationVar(&o.Timeout, "timeout", 0, "whole-action timeout; zero disables it")
	c.Flags().IntVar(&o.ResultFD, "result-fd", -1, "Inherited execution result notification descriptor (>=3)")
	c.RunE = func(c *cobra.Command, args []string) error {
		if o.URL == "" && (c.ArgsLenAtDash() != 0 || len(args) == 0) {
			return errors.New("local record requires --cmd and native arguments after --")
		}
		if o.URL != "" && (c.Flags().Changed("cmd") || c.ArgsLenAtDash() >= 0 || len(args) != 0) {
			return errors.New("remote target excludes --cmd and native arguments")
		}
		if !filepath.IsAbs(o.SessionDir) {
			return errors.New("--session-dir must be absolute")
		}
		if o.Timeout < 0 || o.ConnectTimeout < 0 {
			return errors.New("timeouts must be nonnegative")
		}
		if o.ConnectTimeout > 0 && o.URL == "" {
			return errors.New("connect-timeout requires a remote target")
		}
		if err := validateResultFD(o.ResultFD); err != nil {
			return err
		}
		o.NativeArgs = append([]string(nil), args...)
		return record.Run(c.Context(), o, capture.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
	}
	return c
}

func officialRecordCommand() *cobra.Command {
	var o official.RecordOptions
	c := &cobra.Command{
		Use:   "record [flags] [-- ARG ...]",
		Short: "Record a native Agent interaction in a prepared session",
		Args:  cobra.ArbitraryArgs,
	}
	c.Flags().StringVar(&o.SessionDir, "session-dir", "", "prepared session directory")
	c.Flags().StringVar(&o.Command, "cmd", "", "absolute native Agent executable")
	c.Flags().DurationVar(&o.ConnectTimeout, "connect-timeout", 0, "retry unavailable remote connections for this duration")
	c.Flags().StringVar(&o.URL, "url", "", "existing authenticated ws/wss endpoint (excludes --cmd and native arguments)")
	c.Flags().StringVar(&o.TokenFile, "token-file", "", "private bearer-token file for --url (never persisted)")
	c.Flags().DurationVar(&o.Timeout, "timeout", 0, "whole-action timeout; zero disables it")
	c.RunE = func(c *cobra.Command, args []string) error {
		if o.URL == "" && (c.ArgsLenAtDash() != 0 || len(args) == 0) {
			return errors.New("local record requires --cmd and native arguments after --")
		}
		if o.URL != "" && (c.Flags().Changed("cmd") || c.ArgsLenAtDash() >= 0 || len(args) != 0) {
			return errors.New("remote target excludes --cmd and native arguments")
		}
		if !filepath.IsAbs(o.SessionDir) {
			return errors.New("--session-dir must be absolute")
		}
		if o.Timeout < 0 || o.ConnectTimeout < 0 {
			return errors.New("timeouts must be nonnegative")
		}
		if o.ConnectTimeout > 0 && o.URL == "" {
			return errors.New("connect-timeout requires a remote target")
		}
		o.NativeArgs = append([]string(nil), args...)
		return official.Record(c.Context(), o)
	}
	return c
}

func verifyCommand() *cobra.Command {
	var o official.VerifyOptions
	c := &cobra.Command{
		Use:   "verify [flags]",
		Short: "Verify material in a prepared session",
		Args:  cobra.NoArgs,
	}
	c.Flags().StringVar(&o.SessionDir, "session-dir", "", "prepared session directory")
	c.Flags().DurationVar(&o.Timeout, "timeout", 0, "whole-task timeout; zero disables it")
	c.Flags().StringVar(&o.Index, "index", "", "optional fixed index: qualify and atomically adopt after verification")
	c.Flags().StringVar(&o.Scenarios, "scenarios", "", "scenario catalog for admission (default: index sibling ../scenarios)")
	c.Flags().StringVar(&o.Archive, "archive", "", "private admission archive (default: persistent data directory)")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if o.SessionDir == "" {
			return errors.New("verify requires --session-dir")
		}
		if !filepath.IsAbs(o.SessionDir) {
			return errors.New("--session-dir must be an absolute path")
		}
		return official.Verify(c.Context(), o)
	}
	return c
}

func mockCommand() *cobra.Command {
	return newMockCommand()
}

func peerCommand(streams IO) *cobra.Command {
	var o tool.PeerOptions
	c := &cobra.Command{
		Use:    "internal-peer",
		Short:  "Launch one declared peer process",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(c *cobra.Command, args []string) error {
			if o.Request == "" {
				return errors.New("peer requires --request")
			}
			o.In, o.Out, o.Err = streams.In, streams.Out, streams.Err
			return tool.RunPeer(o)
		},
	}
	c.Flags().StringVar(&o.Request, "request", "", "peer command file")
	c.Flags().StringVar(&o.Results, "results", "", "execution notification evidence for a public agent command")
	c.Flags().StringVar(&o.Lifecycle, "lifecycle", "", "supervised child exit evidence")
	return c
}

func internalArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	// Normalize the hidden process-interface spellings. The single Cobra root
	// still owns command selection, validation and handler execution.
	switch args[0] {
	case "--internal-peer":
		return append([]string{"internal-peer"}, args[1:]...)
	default:
		return args
	}
}

func requireNewSessionDir(sessionDir string) error {
	_, err := os.Lstat(sessionDir)
	switch {
	case err == nil:
		return fmt.Errorf("--session-dir already exists: %s", sessionDir)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return err
	}
}
