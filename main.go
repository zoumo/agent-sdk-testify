// agent-sdk-testify owns environments and process orchestration, never SDK assertions.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/zoumo/agent-sdk-testify/internal/mock"
	"github.com/zoumo/agent-sdk-testify/internal/record"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/verify"
	"github.com/zoumo/agent-sdk-testify/protocol"
	protocolcmd "github.com/zoumo/agent-sdk-testify/protocol/command"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mock" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := mock.Execute(ctx, os.Args[2:])
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
		if err := tool.RunPeer(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := execute(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "record":
			return record.Execute(ctx, args[1:])
		case "verify":
			return verify.Execute(ctx, args[1:])
		}
	}
	return errors.New("expected record, verify or mock")
}
