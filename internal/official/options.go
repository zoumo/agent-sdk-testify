package official

import "github.com/zoumo/agent-sdk-testify/internal/tool"

// Options describes one official-driver execution. The driver and native CLI
// remain explicit inputs; versions and hashes never select a runtime.
type Options struct {
	Mode, Materials, Case, Provider, Output, Workspace, Driver, Peer, CLI, Config, Model string
	OutputPrepared                                                                       bool
	HistoryPrepared                                                                      bool
	HistorySeed                                                                          string
	InitialModel                                                                         string
	DriverArgs                                                                           []string
	NativeCommand                                                                        string
	NativeArgs                                                                           []string
	RuntimeEnv                                                                           map[string]string
	Plan                                                                                 tool.Object
}
