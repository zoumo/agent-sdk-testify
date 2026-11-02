// Package data owns the tracked catalog and material snapshots embedded in the
// installed binary. Runtime code never reads them from a source checkout.
package data

import (
	"embed"
	"io/fs"
)

//go:embed scenarios/*.json scenarios/bindings/*.json
var scenarios embed.FS

//go:embed builtin/manifest.json builtin/materials.tar.gz
var builtinSnapshot embed.FS

func Scenarios() fs.FS {
	root, err := fs.Sub(scenarios, "scenarios")
	if err != nil {
		panic(err)
	}
	return root
}

func Builtin() fs.FS {
	return builtinSnapshot
}
