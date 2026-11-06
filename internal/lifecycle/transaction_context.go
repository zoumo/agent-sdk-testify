package lifecycle

import (
	"maps"
	"path/filepath"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type transactionContext struct {
	session      *Session
	plan         tool.Object
	execution    Execution
	connection   string
	sessionDir   string
	dir          string
	overlayRoot  string
	artifactRoot string
}

func newTransactionContext(session *Session, plan tool.Object, execution Execution, sessionDir string) *transactionContext {
	return &transactionContext{
		session:      session,
		plan:         plan,
		execution:    execution,
		sessionDir:   sessionDir,
		dir:          ExecutionDir(sessionDir, execution.ExecutionID),
		overlayRoot:  filepath.Dir(execution.Home),
		artifactRoot: execution.ArtifactRoot,
	}
}

func (c transactionContext) Session() Session {
	session := *c.session
	session.Tool.Args = slices.Clone(c.session.Tool.Args)
	session.HistorySeedFiles = maps.Clone(c.session.HistorySeedFiles)
	session.Env = maps.Clone(c.session.Env)
	session.Routes = make(map[string]Route, len(c.session.Routes))
	for name, route := range c.session.Routes {
		route.Env = maps.Clone(route.Env)
		route.RuntimeEnv = slices.Clone(route.RuntimeEnv)
		connections := route.Connections
		route.Connections = make([]ConnectionTemplate, len(connections))
		for i, connection := range connections {
			connection.Args = slices.Clone(connection.Args)
			route.Connections[i] = connection
		}
		session.Routes[name] = route
	}
	return session
}

func (c transactionContext) Plan() tool.Object {
	return cloneTransactionObject(c.plan)
}

func (c transactionContext) Execution() Execution {
	return cloneTransactionExecution(c.execution)
}

func (c transactionContext) Artifact() corpus.Material {
	return cloneTransactionMaterial(c.execution.Artifact)
}

func (c transactionContext) Connection() string {
	return c.connection
}

func (c transactionContext) SessionDir() string {
	return c.sessionDir
}

func (c transactionContext) ExecutionDir() string {
	return c.dir
}

func (c transactionContext) OverlayRoot() string {
	return c.overlayRoot
}

func (c transactionContext) ArtifactRoot() string {
	return c.artifactRoot
}

func (c transactionContext) Workspace() string {
	return c.execution.Workspace
}

func (c transactionContext) ConnectionReceipt() string {
	for _, connection := range c.execution.Connections {
		if connection.ID == c.connection {
			return connection.Receipt
		}
	}
	return ""
}

func (c transactionContext) ProcessReceipt() string {
	for _, connection := range c.execution.Connections {
		if connection.ID == c.connection {
			return connection.ProcessReceipt
		}
	}
	return ""
}

func cloneTransactionObject(value tool.Object) tool.Object {
	if value == nil {
		return nil
	}
	clone := make(tool.Object, len(value))
	for key, item := range value {
		clone[key] = cloneTransactionValue(item)
	}
	return clone
}

func cloneTransactionValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneTransactionObject(value)
	case []any:
		clone := make([]any, len(value))
		for i, item := range value {
			clone[i] = cloneTransactionValue(item)
		}
		return clone
	case map[string]string:
		return maps.Clone(value)
	case []string:
		return slices.Clone(value)
	default:
		return value
	}
}

func cloneTransactionExecution(source Execution) Execution {
	execution := source
	execution.Args = slices.Clone(source.Args)
	execution.NativeArgs = slices.Clone(source.NativeArgs)
	execution.HistoryFiles = maps.Clone(source.HistoryFiles)
	execution.Env = maps.Clone(source.Env)
	execution.RuntimeEnv = maps.Clone(source.RuntimeEnv)
	execution.Connections = make([]ExecutionConnection, len(source.Connections))
	for i, connection := range source.Connections {
		connection.Args = slices.Clone(connection.Args)
		execution.Connections[i] = connection
	}
	if source.Admission != nil {
		admission := *source.Admission
		execution.Admission = &admission
	}
	execution.Artifact = cloneTransactionMaterial(source.Artifact)
	return execution
}

func cloneTransactionMaterial(source corpus.Material) corpus.Material {
	material := source
	material.Files = maps.Clone(source.Files)
	material.Connections = slices.Clone(source.Connections)
	if source.OfficialOrigin != nil {
		origin := *source.OfficialOrigin
		origin.Connections = slices.Clone(source.OfficialOrigin.Connections)
		material.OfficialOrigin = &origin
	}
	return material
}
