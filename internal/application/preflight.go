package application

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/mock"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/record"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func openPreparedFixture(fixtureDir string) (*lifecycle.Fixture, string, error) {
	dir, err := lifecycle.CanonicalFixtureDir(fixtureDir)
	if err != nil {
		return nil, "", err
	}
	fixture, err := lifecycle.OpenFixture(dir)
	if err != nil {
		return nil, "", err
	}
	return fixture, dir, nil
}

func prepareRecord(o RecordOptions) (record.Prepared, error) {
	if o.Connection == "" {
		o.Connection = "main"
	}
	var executable string
	var remote *capture.ConnectOptions
	var err error
	if o.URL != "" || o.TokenFile != "" {
		if o.URL == "" || o.TokenFile == "" || o.Command != "" || len(o.NativeArgs) != 0 {
			return record.Prepared{}, errors.New("remote record requires --url and --token-file, excluding local command and arguments")
		}
		remote = &capture.ConnectOptions{
			URL:            o.URL,
			TokenFile:      o.TokenFile,
			ConnectTimeout: o.ConnectTimeout,
		}
		if err := remote.Validate(); err != nil {
			return record.Prepared{}, err
		}
	} else {
		executable, err = tool.ValidateCommand(o.Command)
		if err != nil {
			return record.Prepared{}, err
		}
	}
	fixture, fixtureDir, err := openPreparedFixture(o.FixtureDir)
	if err != nil {
		return record.Prepared{}, err
	}
	if remote != nil {
		if err := validateTokenLocation(fixtureDir, remote.TokenFile); err != nil {
			return record.Prepared{}, err
		}
	} else if err := protocol.ValidateNativeArgs(fixture.AgentType, o.NativeArgs); err != nil {
		return record.Prepared{}, err
	}
	plan, err := lifecycle.LoadPlan(fixture)
	if err != nil {
		return record.Prepared{}, err
	}
	if !slices.Contains(lifecycle.ConnectionIDs(fixture.Source, plan), o.Connection) {
		return record.Prepared{}, fmt.Errorf("undeclared record connection %s", o.Connection)
	}
	args := append([]string{"--fixture-dir", fixtureDir, "--cmd", executable, "--"}, o.NativeArgs...)
	target := "local"
	if remote != nil {
		// Connection location and authentication paths are invocation-only data.
		args = []string{"--fixture-dir", fixtureDir, "--url", "<private>", "--token-file", "<private>"}
		token, err := os.ReadFile(remote.TokenFile)
		if err != nil {
			return record.Prepared{}, err
		}
		target = tool.DigestBytes(append(append([]byte(remote.URL), 0), token...))
	}
	return record.Prepared{
		FixtureDir: fixtureDir,
		Connection: o.Connection,
		Args:       args,
		NativeArgs: slices.Clone(o.NativeArgs),
		Command:    executable,
		Remote:     remote,
		Target:     target,
		Timeout:    o.Timeout,
		ResultFD:   o.ResultFD,
	}, nil
}

func prepareMock(o MockOptions) (mock.Prepared, error) {
	return prepareMockMode(o, false)
}

func prepareInProcessMock(o MockOptions) (mock.Prepared, error) {
	return prepareMockMode(o, true)
}

func prepareMockMode(o MockOptions, inProcess bool) (mock.Prepared, error) {
	if o.Connection == "" {
		o.Connection = "main"
	}
	fixture, fixtureDir, err := openPreparedFixture(o.FixtureDir)
	if err != nil {
		return mock.Prepared{}, err
	}
	if inProcess && fixture.Tool.Source != lifecycle.ToolSourceEmbedded {
		return mock.Prepared{}, errors.New("in-process mock requires embedded fixture")
	}
	if !inProcess && fixture.Tool.Source != lifecycle.ToolSourceExecutable {
		return mock.Prepared{}, errors.New("process mock requires executable fixture")
	}
	if err := protocol.ValidateNativeArgs(fixture.AgentType, o.NativeArgs); err != nil {
		return mock.Prepared{}, err
	}
	material, err := currentMaterial(fixtureDir, fixture)
	if err != nil {
		return mock.Prepared{}, err
	}
	if !slices.Contains(material.Connections, o.Connection) {
		return mock.Prepared{}, fmt.Errorf("undeclared mock connection %s", o.Connection)
	}
	if inProcess {
		transport := ""
		for _, connection := range fixture.Routes["mock"].Connections {
			if connection.ID == o.Connection {
				transport = connection.Transport
				break
			}
		}
		if transport != lifecycle.ConnectionTransportInProcess {
			return mock.Prepared{}, fmt.Errorf("mock connection %s is not in-process", o.Connection)
		}
	}
	if err := mock.ValidateMaterial(*material, fixture.AgentType); err != nil {
		return mock.Prepared{}, err
	}
	return mock.Prepared{
		FixtureDir:  fixtureDir,
		Connection:  o.Connection,
		ExecutionID: o.ExecutionID,
		Agent:       fixture.AgentType,
		Artifact:    *material,
		Timeout:     o.Timeout,
		ResultFD:    o.ResultFD,
		ProgressFD:  o.ProgressFD,
		NativeArgs:  slices.Clone(o.NativeArgs),
	}, nil
}

func currentMaterial(fixtureDir string, fixture *lifecycle.Fixture) (*corpus.Material, error) {
	material, present, err := lifecycle.CurrentArtifact(fixtureDir, fixture)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("fixture has no current recording")
	}
	return material, nil
}

func validateTokenLocation(fixtureDir, tokenFile string) error {
	tokenPath, err := filepath.EvalSymlinks(tokenFile)
	if err != nil {
		return errors.New("private token file unavailable")
	}
	if relative, err := filepath.Rel(fixtureDir, tokenPath); err == nil && filepath.IsLocal(relative) {
		runtimeRelative, err := filepath.Rel(filepath.Join(fixtureDir, "runtime"), tokenPath)
		if err != nil || !filepath.IsLocal(runtimeRelative) {
			return errors.New("token file must be outside fixture evidence (use runtime or an external file)")
		}
	}
	return nil
}
