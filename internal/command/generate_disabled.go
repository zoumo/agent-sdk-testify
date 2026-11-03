//go:build !builtin_generate

package command

import "github.com/spf13/cobra"

func registerGenerate(*cobra.Command, IO) {}
