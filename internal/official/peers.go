package official

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Launchers adapt SDKs that accept only an executable path. The runner alone
// chooses the protocol command, inputs and receipt for each independent run.
func WritePeerLauncher(root, phase string, peer tool.Command) (string, error) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	words := []string{quote(peer.Command)}
	for _, arg := range peer.Args {
		words = append(words, quote(arg))
	}
	path := filepath.Join(root, "peer-"+phase)
	return path, os.WriteFile(path, []byte("#!/bin/sh\nexec "+strings.Join(words, " ")+"\n"), 0o700)
}
