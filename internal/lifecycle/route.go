package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// ResolveRoute is shared by public Go runtime objects and private CLI snapshots.
// Callers coordinate the complete SDK host lifetime before reading either view.
func ResolveRoute(fixture *Fixture, action string) (Route, error) {
	route, ok := fixture.Routes[action]
	if !ok {
		return Route{}, errors.New("unknown fixture route")
	}
	root := fixture.dir
	route.Workspace = RouteWorkspace(root, action)
	route.Env = baseEnvironment(fixture.AgentType, RouteRoot(root, action))
	route.RuntimeEnv = slices.Clone(route.RuntimeEnv)
	route.Connections = slices.Clone(route.Connections)
	if route.ProtocolCwd == "" {
		route.ProtocolCwd = route.Workspace
	}
	for i := range route.Connections {
		connection := &route.Connections[i]
		if connection.Transport == ConnectionTransportProcess {
			connection.Command = fixture.Tool.Command
			connection.Args, connection.Launcher = connectionLaunch(root, action, connection.ID)
		}
	}
	return route, nil
}

// RefreshRoutes writes disposable, non-secret startup views. Persistent evidence
// never references these files, and no snapshot field supplies mutual exclusion.
func RefreshRoutes(fixture *Fixture) error {
	return refreshPreparedRoutes(fixture, fixture.dir)
}

func refreshPreparedRoutes(fixture *Fixture, publishedDir string) error {
	current, err := readCurrent(fixture.dir, fixture.FixtureID)
	if err != nil {
		return err
	}
	for _, action := range []string{"record", "mock"} {
		route, err := ResolveRoute(fixture, action)
		if err != nil {
			return err
		}
		historyFiles := map[string]string{}
		source := ""
		if action == "mock" && current.MaterialID != "" {
			source = MaterialRoot(fixture.dir, current.MaterialID)
		}
		if action == "record" && fixture.HistorySeed != "" {
			if err := VerifyHistorySeed(fixture); err != nil {
				return err
			}
			source = history.SeedSource(fixture.HistorySeed)
		}
		if source != "" {
			template := TemplateRoot(fixture.dir, action)
			reset := history.Reset
			if action == "record" {
				reset = history.ResetNative
			}
			if err := reset(template); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(template, "history-seed"), 0o700); err != nil {
				return err
			}
			historyFiles, err = history.MaterializeStartup(source, template, RouteWorkspace(publishedDir, action))
			if err != nil {
				return err
			}
		}

		if publishedDir != fixture.dir {
			published := *fixture
			published.dir = publishedDir
			// A default protocol cwd follows its workspace. Recorded protocol
			// identities remain untouched, including absolute original projects.
			if route.ProtocolCwd == route.Workspace {
				route.ProtocolCwd = RouteWorkspace(publishedDir, action)
			}
			published.Routes = map[string]Route{action: route}
			route, err = ResolveRoute(&published, action)
			if err != nil {
				return err
			}
		}
		connections := make([]tool.Object, 0, len(route.Connections))
		for _, connection := range route.Connections {
			value := tool.Object{"id": connection.ID, "transport": connection.Transport}
			if connection.Transport == ConnectionTransportProcess {
				value["command"], value["args"] = connection.Command, connection.Args
				if connection.Launcher != "" {
					value["launcher"] = connection.Launcher
				}
			}
			connections = append(connections, value)
		}
		snapshot := tool.Object{"schemaVersion": 1, "fixtureId": fixture.FixtureID, "action": action, "enabled": route.Enabled, "materialId": current.MaterialID, "workspace": route.Workspace, "protocolCwd": route.ProtocolCwd, "env": route.Env, "runtimeEnv": route.RuntimeEnv, "historyFiles": historyFiles, "connections": connections}
		if err := os.MkdirAll(filepath.Join(fixture.dir, "runtime", "routes"), 0o700); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(fixture.dir, "runtime", "routes", action+".json"), snapshot); err != nil {
			return err
		}
	}
	return nil
}
