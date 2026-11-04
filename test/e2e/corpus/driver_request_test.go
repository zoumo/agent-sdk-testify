package corpus

import "github.com/zoumo/agent-sdk-testify/internal/tool"

type driverPeer struct {
	tool.Command
	Probe *tool.Command `json:"probe,omitempty"`
}

type driverRequest struct {
	Case      string     `json:"case"`
	Materials string     `json:"materials"`
	Workspace string     `json:"workspace"`
	Peer      driverPeer `json:"peer"`
	Output    string     `json:"output"`
}
