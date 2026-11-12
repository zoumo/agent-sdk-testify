package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

func TestFixtureValidateRejectsPriorSchemas(t *testing.T) {
	for _, schemaVersion := range []int{1, 2, 3, 4, 5} {
		t.Run(strconv.Itoa(schemaVersion), func(t *testing.T) {
			err := (Fixture{SchemaVersion: schemaVersion}).Validate()
			require.EqualError(t, err, "unsupported fixture schemaVersion "+strconv.Itoa(schemaVersion)+"; schema 6 is required")
		})
	}
}

func TestToolIdentityValidatesExecutableAndEmbeddedUnions(t *testing.T) {
	executable := ToolIdentity{
		Source:  ToolSourceExecutable,
		Version: "0.1.0",
		Command: "/private/tool",
		SHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	embedded := ToolIdentity{
		Source:         ToolSourceEmbedded,
		Version:        "v0.0.0-20261108181220-c4d2e2a5cdd6",
		Module:         version.Module,
		ModuleRevision: "c4d2e2a5cdd67f61ef41b7932b66ca454c4077a8",
		ModuleTime:     "2026-11-08T18:12:20Z",
		GoVersion:      "go1.26.10",
	}
	dependency := ToolIdentity{
		Source:    ToolSourceEmbedded,
		Version:   "v0.2.0",
		Module:    version.Module,
		ModuleSum: "h1:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
		GoVersion: "go1.26.10",
	}
	for _, test := range []struct {
		name     string
		identity ToolIdentity
		want     string
	}{
		{name: "executable", identity: executable},
		{name: "embedded main module", identity: embedded},
		{name: "embedded dependency", identity: dependency},
		{
			name: "executable module fields",
			identity: ToolIdentity{
				Source: executable.Source, Version: executable.Version, Command: executable.Command,
				SHA256: executable.SHA256, Module: version.Module,
			},
			want: "declares embedded module fields",
		},
		{
			name: "executable module time",
			identity: ToolIdentity{
				Source: executable.Source, Version: executable.Version, Command: executable.Command,
				SHA256: executable.SHA256, ModuleTime: embedded.ModuleTime,
			},
			want: "declares embedded module fields",
		},
		{
			name: "relative executable",
			identity: ToolIdentity{
				Source: executable.Source, Version: executable.Version, Command: "tool", SHA256: executable.SHA256,
			},
			want: "requires absolute command",
		},
		{
			name: "invalid executable digest",
			identity: ToolIdentity{
				Source: executable.Source, Version: executable.Version, Command: executable.Command, SHA256: "not-a-digest",
			},
			want: "SHA-256 is invalid",
		},
		{
			name: "embedded executable fields",
			identity: ToolIdentity{
				Source: embedded.Source, Version: embedded.Version, Command: embedded.Module,
				Module: embedded.Module, ModuleRevision: embedded.ModuleRevision, GoVersion: embedded.GoVersion,
			},
			want: "declares executable fields",
		},
		{
			name: "wrong module",
			identity: ToolIdentity{
				Source: embedded.Source, Version: embedded.Version, Module: "example.com/other",
				ModuleRevision: embedded.ModuleRevision, GoVersion: embedded.GoVersion,
			},
			want: "module must be",
		},
		{
			name: "inexact version",
			identity: ToolIdentity{
				Source: embedded.Source, Version: "0.1.0", Module: embedded.Module,
				ModuleRevision: embedded.ModuleRevision, GoVersion: embedded.GoVersion,
			},
			want: "is not exact",
		},
		{
			name: "unproven identity",
			identity: ToolIdentity{
				Source: embedded.Source, Version: embedded.Version, Module: embedded.Module,
				GoVersion: embedded.GoVersion,
			},
			want: "neither an exact revision nor a module sum",
		},
		{
			name: "invalid module time",
			identity: ToolIdentity{
				Source: embedded.Source, Version: embedded.Version, Module: embedded.Module,
				ModuleRevision: embedded.ModuleRevision, ModuleTime: "yesterday", GoVersion: embedded.GoVersion,
			},
			want: "module time is invalid",
		},
		{
			name: "development identity without revision",
			identity: ToolIdentity{
				Source: embedded.Source, Version: "(devel)", Module: embedded.Module,
				ModuleSum: dependency.ModuleSum, GoVersion: embedded.GoVersion,
			},
			want: "development identity requires an exact module revision",
		},
		{
			name: "invalid module sum",
			identity: ToolIdentity{
				Source: embedded.Source, Version: dependency.Version, Module: embedded.Module,
				ModuleSum: "h1:not-a-sum", GoVersion: embedded.GoVersion,
			},
			want: "module sum is invalid",
		},
		{
			name: "unsupported source",
			identity: ToolIdentity{
				Version: executable.Version,
			},
			want: `unsupported tool source ""`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.identity.Validate()
			if test.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestFixtureConnectionIdentityEnforcesDiscriminants(t *testing.T) {
	executable := Fixture{Tool: ToolIdentity{Source: ToolSourceExecutable}}
	embedded := Fixture{Tool: ToolIdentity{Source: ToolSourceEmbedded}}

	require.NoError(t, executable.validateConnectionTemplate(ConnectionTemplate{
		ID: "main", Transport: ConnectionTransportProcess, Command: "/private/tool", Args: []string{"agent"},
	}))
	require.ErrorContains(t, executable.validateConnectionTemplate(ConnectionTemplate{
		ID: "main", Transport: ConnectionTransportInProcess,
	}), "executable fixture declares an in-process connection")
	require.NoError(t, embedded.validateConnectionTemplate(ConnectionTemplate{
		ID: "main", Transport: ConnectionTransportInProcess,
	}))
	require.ErrorContains(t, embedded.validateConnectionTemplate(ConnectionTemplate{
		ID: "main", Transport: ConnectionTransportProcess, Command: "/private/tool",
	}), "embedded fixture declares a process connection")
	require.ErrorContains(t, embedded.validateConnectionTemplate(ConnectionTemplate{
		ID: "main", Transport: ConnectionTransportInProcess, Command: version.Module,
	}), "in-process connection declares executable fields")
	require.ErrorContains(t, embedded.validateConnectionTemplate(ConnectionTemplate{
		ID: "main", Transport: "socket",
	}), `unsupported connection transport "socket"`)
}

func TestConnectionDecodeRejectsMixedSchemaFiveFields(t *testing.T) {
	t.Run("legacy transport remains decodable for schema rejection", func(t *testing.T) {
		var connection ConnectionTemplate
		require.NoError(t, json.Unmarshal([]byte(`{"id":"main","command":"legacy","embedded":true}`), &connection))
	})

	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{
			name: "in-process command key",
			data: `{"id":"main","transport":"in-process","command":""}`,
			want: "in-process connection declares executable fields",
		},
		{
			name: "in-process argv key",
			data: `{"id":"main","transport":"in-process","argv":[]}`,
			want: "in-process connection declares executable fields",
		},
		{
			name: "embedded marker",
			data: `{"id":"main","transport":"in-process","embedded":true}`,
			want: `unsupported connection field "embedded"`,
		},
		{
			name: "connection SHA",
			data: `{"id":"main","transport":"process","sha256":"digest"}`,
			want: `unsupported connection field "sha256"`,
		},
		{
			name: "process command",
			data: `{"id":"main","transport":"process","command":"/private/tool"}`,
			want: `unsupported connection field "command"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var connection ConnectionTemplate
			err := json.Unmarshal([]byte(test.data), &connection)
			require.ErrorContains(t, err, test.want)
		})
	}

	var executionConnection ExecutionConnection
	err := json.Unmarshal([]byte(`{"id":"main","transport":"in-process","args":[]}`), &executionConnection)
	require.ErrorContains(t, err, "in-process connection declares executable fields")
}

func TestDescriptorReadersRejectPriorSchemas(t *testing.T) {
	t.Run("execution", func(t *testing.T) {
		err := (Execution{SchemaVersion: 4}).Validate(Fixture{})
		require.EqualError(t, err, "unsupported execution schemaVersion 4; schema 6 is required")
	})

	t.Run("execution index", func(t *testing.T) {
		root := t.TempDir()
		path := IndexPath(root)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, tool.WriteJSON(path, Index{SchemaVersion: 4}))
		_, err := readIndex(path)
		require.EqualError(t, err, "unsupported execution index schemaVersion 4; schema 6 is required")
	})

	t.Run("material index", func(t *testing.T) {
		root := t.TempDir()
		path := IndexPath(root)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Dir(MaterialIndexPath(root)), 0o700))
		require.NoError(t, tool.WriteJSON(path, Index{SchemaVersion: 6, FixtureID: "fixture", FixtureSHA256: "digest"}))
		require.NoError(t, tool.WriteJSON(MaterialIndexPath(root), MaterialIndex{SchemaVersion: 4, FixtureID: "fixture"}))
		_, err := readIndex(path)
		require.EqualError(t, err, "unsupported material index schemaVersion 4; schema 6 is required")
	})

	t.Run("current recording", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "current.json"), []byte(`{"schemaVersion":4,"fixtureId":"fixture"}`), 0o600))
		fixture := &Fixture{FixtureID: "fixture"}
		_, _, err := CurrentArtifact(root, fixture)
		require.EqualError(t, err, "unsupported current recording schemaVersion 4; schema 6 is required")
	})

	t.Run("open fixture with prior current recording", func(t *testing.T) {
		fixtureDir := prepareRecordTransactionFixture(t)
		var current Current
		path := filepath.Join(fixtureDir, "current.json")
		require.NoError(t, tool.ReadJSON(path, &current))
		current.SchemaVersion = 4
		require.NoError(t, tool.WriteJSON(path, current))
		_, err := OpenFixture(fixtureDir)
		require.EqualError(t, err, "unsupported current recording schemaVersion 4; schema 6 is required")
	})
}
