package corpus

import "github.com/zoumo/agent-sdk-testify/internal/tool"

type driverPeer struct {
	tool.Command
	Probe *tool.Command `json:"probe,omitempty"`
}

type driverRequest struct {
	Case      string `json:"case"`
	Materials string `json:"materials"`
	Runtime   string `json:"runtime"`
}

type driverRuntime struct {
	SchemaVersion float64    `json:"schemaVersion"`
	Workspace     string     `json:"workspace"`
	Output        string     `json:"output"`
	Peer          driverPeer `json:"peer"`
}
