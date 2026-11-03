// agent-sdk-testify owns environments and process orchestration, never SDK assertions.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/zoumo/agent-sdk-testify/internal/command"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := command.Execute(ctx, os.Args[1:], command.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
	stop()
	os.Exit(code)
}
