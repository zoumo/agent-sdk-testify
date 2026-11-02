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

	"github.com/zoumo/agent-sdk-testify/protocol"
	protocolcmd "github.com/zoumo/agent-sdk-testify/protocol/command"
)

type command struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}
type driverPeer struct {
	command
	Probe *command `json:"probe,omitempty"`
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
	OutputPrepared                                                            bool
	HistorySeed                                                               string
	InitialModel                                                              string
	DriverArgs                                                                []string
	Timeout                                                                   time.Duration
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mock" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := executeMock(ctx, os.Args[2:])
		stop()
		os.Exit(code)
	}
	if len(os.Args) > 1 && os.Args[1] == "--internal-protocol" {
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
			fmt.Println("Usage: agent-sdk-testify record|verify|mock [flags]\n       agent-sdk-testify --version")
			return
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "--internal-peer" {
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
	if len(args) > 0 {
		switch args[0] {
		case "record":
			return executeRecord(ctx, args[1:])
		case "verify":
			return executeVerify(ctx, args[1:])
		}
	}
	return errors.New("expected record, verify or mock")
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
func runPeer(args []string) (runErr error) {
	f := flag.NewFlagSet("peer", flag.ContinueOnError)
	p := f.String("request", "", "peer command file")
	lifecycle := f.String("lifecycle", "", "internal protocol child exit evidence")
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
	if *lifecycle != "" {
		defer func() {
			// This wrapper waits for its only child on every exit path.
			code := cmd.ProcessState.ExitCode()
			report := object{"schemaVersion": 1, "exitCode": code, "reaped": cmd.ProcessState != nil, "cleanupComplete": true}
			if runErr != nil {
				report["error"] = runErr.Error()
			}
			runErr = errors.Join(runErr, writeJSON(*lifecycle, report))
		}()
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
