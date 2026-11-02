package record

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type (
	request = official.Request
	command = tool.Command
	object  = tool.Object
	result  = tool.Result
)

var (
	obj       = tool.Obj
	str       = tool.Str
	readJSON  = tool.ReadJSON
	writeJSON = tool.WriteJSON
)

func writePeerLauncher(root, phase string, peer command) (string, error) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	words := []string{quote(peer.Command)}
	for _, arg := range peer.Args {
		words = append(words, quote(arg))
	}
	path := filepath.Join(root, "peer-"+phase)
	return path, os.WriteFile(path, []byte("#!/bin/sh\nexec "+strings.Join(words, " ")+"\n"), 0o700)
}
