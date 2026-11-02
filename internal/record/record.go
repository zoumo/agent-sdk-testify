// Package record owns the public record command. Official runtime preparation
// and driver execution stay in internal/official.
package record

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/zoumo/agent-sdk-testify/internal/official"
)

func Execute(ctx context.Context, args []string) error {
	o := official.Options{Mode: "record"}
	f := flag.NewFlagSet("record", flag.ContinueOnError)
	f.StringVar(&o.Provider, "provider", "", "native provider: codex or claude")
	f.StringVar(&o.Case, "case", "", "case ID from this tool's catalog")
	f.StringVar(&o.CLI, "cli", "", "installed native Agent executable")
	f.StringVar(&o.Config, "config", "", "selected native configuration directory")
	f.StringVar(&o.Output, "output", "", "new private capture directory (default: persistent attempt)")
	f.StringVar(&o.Model, "model", "", "native model")
	f.StringVar(&o.InitialModel, "initial-model", "", "initial model for a change to --model")
	f.StringVar(&o.HistorySeed, "history-seed", "", "completed child-history capture for existing-child cases")
	f.DurationVar(&o.Timeout, "timeout", 0, "whole-task timeout; zero disables it")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || o.Case == "" || o.CLI == "" {
		return errors.New("record requires --provider, --case and --cli")
	}
	if o.Timeout < 0 {
		return errors.New("timeout must be nonnegative")
	}
	var err error
	o.Driver, err = official.ResolveDriver(o.Provider)
	if err != nil {
		return err
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	summary, err := official.Run(ctx, o)
	if summary != nil {
		b, _ := json.Marshal(summary)
		fmt.Fprintln(os.Stdout, string(b))
	}
	return err
}
