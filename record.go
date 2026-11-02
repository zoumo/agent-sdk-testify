package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Record and mock share this authoritative catalog. Generated builtin materials
// bind these exact contract/binding bytes; there is no second authoring source.
//
//go:embed tests/simulation/scenarios/*.json tests/simulation/scenarios/bindings/*.json
var catalogFiles embed.FS

var caseCatalog = func() fs.FS {
	catalog, err := fs.Sub(catalogFiles, "tests/simulation/scenarios")
	if err != nil {
		panic(err)
	}
	return catalog
}()

func executeRecord(ctx context.Context, args []string) error {
	o := options{Mode: "record"}
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
	o.Driver, err = resolveOfficialDriver(o.Provider)
	if err != nil {
		return err
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	summary, err := run(ctx, o)
	if summary != nil {
		b, _ := json.Marshal(summary)
		fmt.Fprintln(os.Stdout, string(b))
	}
	return err
}

// Keep explicitly selected legacy material/batch workflows until T8 removes
// legacy modes together. The catalog-backed public command has no driver flag.
func legacyRecordSelection(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
		if name == "materials" || name == "cases-file" || name == "index" {
			return true
		}
	}
	return false
}
