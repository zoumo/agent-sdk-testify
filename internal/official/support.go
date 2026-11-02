package official

import (
	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Options describes one official-driver execution. The driver and native CLI
// remain explicit inputs; versions and hashes never select a runtime.
type Options struct {
	Mode, Materials, Case, Provider, Output, Driver, Peer, CLI, Config, Model string
	OutputPrepared                                                            bool
	HistorySeed                                                               string
	InitialModel                                                              string
	DriverArgs                                                                []string
}

type DriverPeer struct {
	tool.Command
	Probe *tool.Command `json:"probe,omitempty"`
}

type Request struct {
	Case      string     `json:"case"`
	Materials string     `json:"materials"`
	Workspace string     `json:"workspace"`
	Peer      DriverPeer `json:"peer"`
	Output    string     `json:"output"`
}

type (
	options    = Options
	request    = Request
	driverPeer = DriverPeer
	object     = tool.Object
	result     = tool.Result
	command    = tool.Command
)

var (
	obj                    = tool.Obj
	str                    = tool.Str
	array                  = tool.Array
	stringsOf              = tool.Strings
	readJSON               = tool.ReadJSON
	writeJSON              = tool.WriteJSON
	fileDigest             = tool.FileDigest
	absoluteExecutable     = tool.AbsoluteExecutable
	waitDriver             = tool.WaitDriver
	createOutput           = tool.CreateOutput
	copyFile               = tool.CopyFile
	writeFiles             = tool.WriteFiles
	copySaved              = tool.CopySaved
	verifyFile             = corpus.VerifyFile
	verifyMaterials        = corpus.VerifyMaterials
	writeSourceManifest    = corpus.WriteSourceManifest
	scanCaptureCredentials = corpus.ScanCaptureCredentials
	materializeClaude      = history.MaterializeClaude
)

func loadMaterials(o options) (tool.Object, error) {
	return catalog.Resolve(data.Scenarios(), catalog.ResolveInput{
		Mode:      o.Mode,
		Materials: o.Materials,
		Case:      o.Case,
		Provider:  o.Provider,
		Model:     o.Model,
	})
}
