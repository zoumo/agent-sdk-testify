// agent-sdk-testify owns environments and process orchestration, never SDK assertions.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol"
	protocolcmd "github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/command"
)

type command struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}
type remoteEndpoint struct {
	URL         string `json:"url"`
	BearerToken string `json:"bearerToken"`
}
type driverPeer struct {
	command
	Probe    *command        `json:"probe,omitempty"`
	Suppress *command        `json:"suppress,omitempty"`
	Remote   *remoteEndpoint `json:"remote,omitempty"`
}
type request struct {
	Case      string     `json:"case"`
	Materials string     `json:"materials"`
	Workspace string     `json:"workspace"`
	Peer      driverPeer `json:"peer"`
	Output    string     `json:"output"`
}
type result struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
type options struct {
	Mode, Materials, Case, Provider, Output, Driver, Peer, CLI, Config, Model string
	HistorySeed                                                               string
	InitialModel                                                              string
	CasesFile                                                                 string
	Jobs                                                                      int
	DriverArgs                                                                []string
	Timeout                                                                   time.Duration
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "protocol" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := protocol.Execute(ctx, os.Args[2:], protocolcmd.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
		stop()
		os.Exit(code)
	}
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "version", "--version":
			fmt.Println("agent-sdk-testify " + protocolcmd.Version)
			return
		case "--help", "-h":
			fmt.Println("Usage: agent-sdk-testify record|replay|simulate [flags]\n       agent-sdk-testify corpus review|qualify|adopt|gate [flags]\n       agent-sdk-testify protocol record|mock|connect [flags]\n       agent-sdk-testify version")
			return
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "peer" {
		if err := runPeer(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("use record, replay or simulate with --materials and --driver; use protocol for standalone protocol operations")
	}
	if args[0] == "corpus" {
		return executeCorpus(ctx, args[1:])
	}
	o := options{Mode: args[0]}
	if o.Mode != "record" && o.Mode != "replay" && o.Mode != "simulate" {
		return errors.New("expected record, replay or simulate")
	}
	f := flag.NewFlagSet(o.Mode, flag.ContinueOnError)
	c := corpus{}
	if o.Mode == "record" {
		c.flags(f)
	}
	f.StringVar(&o.Materials, "materials", "", "shared case directory or existing private recording")
	f.StringVar(&o.CasesFile, "cases-file", "", "JSON list of selected recording paths; optional per-case driver command")
	f.IntVar(&o.Jobs, "jobs", 1, "parallel cases in a batch")
	f.StringVar(&o.Case, "case", "", "case ID when selecting shared materials")
	f.StringVar(&o.Provider, "provider", "", "native provider (inferred from an existing recording)")
	f.StringVar(&o.Output, "output", "", "new output directory (default: persistent AGENT_SDK_TESTIFY_DATA_DIR/attempts/<mode>)")
	f.StringVar(&o.Driver, "driver", "", "installed driver executable or interpreter")
	f.Func("driver-arg", "driver prefix argument; repeat for an interpreter/script", func(s string) error { o.DriverArgs = append(o.DriverArgs, s); return nil })
	self, err := os.Executable()
	if err != nil {
		return err
	}
	f.StringVar(&o.Peer, "peer", self, "agent-sdk-testify executable (default: this binary)")
	f.StringVar(&o.CLI, "cli", "", "native CLI (record only)")
	f.StringVar(&o.Config, "config", "", "selected native configuration directory (record only)")
	f.StringVar(&o.Model, "model", "", "native model (record only)")
	f.StringVar(&o.InitialModel, "initial-model", "", "initial native model for a change to --model (record only)")
	f.StringVar(&o.HistorySeed, "history-seed", "", "completed child-history capture used by an existing-child case")
	f.DurationVar(&o.Timeout, "timeout", 0, "optional whole-task timeout; zero disables it")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || (o.Materials == "" && o.CasesFile == "") || (o.Driver == "" && o.CasesFile == "") || o.Peer == "" {
		return errors.New("materials (or cases-file), driver and peer are required")
	}
	if o.Jobs < 1 {
		return errors.New("jobs must be positive")
	}
	if o.Timeout < 0 {
		return errors.New("timeout must be nonnegative")
	}
	if c.Index != "" {
		if err := c.normalize(); err != nil {
			return err
		}
	} else if c.Scenarios != "" {
		return errors.New("scenarios requires index")
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	var summary object
	if c.Index != "" {
		summary, err = c.recordAndAdopt(ctx, o)
	} else if o.CasesFile != "" {
		summary, err = runBatch(ctx, o)
	} else {
		summary, err = run(ctx, o)
	}
	if summary != nil {
		printed := summary
		if o.CasesFile != "" && c.Index == "" {
			printed = object{"selected": summary["selected"], "passed": summary["passed"], "output": summary["output"]}
		}
		b, _ := json.Marshal(printed)
		fmt.Println(string(b))
	}
	return err
}

func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func absoluteExecutable(name string) (string, error) {
	p, e := exec.LookPath(name)
	if e != nil {
		return "", e
	}
	return filepath.Abs(p)
}

// A peer is a stdio child of the SDK. Give EOF a chance to drain before SDK
// shutdown's TERM cancels it. This process never selects a real-agent fallback.
func runPeer(args []string) error {
	f := flag.NewFlagSet("peer", flag.ContinueOnError)
	p := f.String("request", "", "peer command file")
	if e := f.Parse(args); e != nil {
		return e
	}
	var c command
	if e := readJSON(*p, &c); e != nil {
		return e
	}
	cmd := exec.Command(c.Command, c.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	if e := cmd.Start(); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e := <-done:
		return e
	case sig := <-ch:
		select {
		case e := <-done:
			return e
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Process.Signal(sig)
		select {
		case e := <-done:
			return e
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			return <-done
		}
	}
}
