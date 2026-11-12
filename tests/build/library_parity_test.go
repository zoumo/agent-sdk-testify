package build

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

const libraryModule = "github.com/zoumo/agent-sdk-testify"

func TestInstalledCLIAndLibraryShareFixtureAndMaterialContract(t *testing.T) {
	binary := buildCLI(t, false)
	root := t.TempDir()
	home, config := filepath.Join(root, "host-home"), filepath.Join(root, "host-config")
	require.NoError(t, os.Mkdir(home, 0o700))
	require.NoError(t, os.Mkdir(config, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	emptyPath := filepath.Join(root, "empty-path")
	require.NoError(t, os.Mkdir(emptyPath, 0o700))
	t.Setenv("PATH", emptyPath)

	cliFixture := filepath.Join(root, "cli-fixture")
	runCLI(t, binary, "prepare", "--agent", "codex", "--case", "SDK-CASE-001", "--fixture-dir", cliFixture)
	libraryFixture := filepath.Join(root, "library-fixture")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: libraryFixture,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	require.NotNil(t, prepared.Current)

	cliFixture, err = filepath.EvalSymlinks(cliFixture)
	require.NoError(t, err)
	assertFixtureToolSource(t, cliFixture, "executable")
	assertFixtureToolSource(t, prepared.FixtureDir, "embedded")
	cliOpened, err := agenttestify.Open(cliFixture)
	require.NoError(t, err)
	assertPublicFixtureIdentity(t, cliOpened, agenttestify.ToolSourceExecutable)
	assertPublicFixtureIdentity(t, prepared, agenttestify.ToolSourceEmbedded)
	require.FileExists(t, filepath.Join(cliFixture, "runtime", "tool.json"))
	require.NoFileExists(t, filepath.Join(prepared.FixtureDir, "runtime", "tool.json"))
	require.Equal(t, sharedLayout(t, cliFixture, cliOpened.Current.ID), sharedLayout(t, prepared.FixtureDir, prepared.Current.ID))
	require.Equal(t, normalizedFixture(t, cliFixture), normalizedFixture(t, prepared.FixtureDir))
	assertDescriptorSchemas(t, cliFixture, prepared.FixtureDir)

	cliExport, libraryExport := filepath.Join(root, "cli-export"), filepath.Join(root, "library-export")
	runCLI(t, binary, "export", "--fixture-dir", cliFixture, "--output", cliExport)
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{
		FixtureDir: prepared.FixtureDir,
		Output:     libraryExport,
	}))
	assertEquivalentMaterials(t, cliExport, libraryExport)

	var cliDescriptor struct {
		Source struct {
			SHA256 string `json:"sha256"`
		} `json:"source"`
	}
	require.NoError(t, readJSON(filepath.Join(cliFixture, "fixture.json"), &cliDescriptor))
	require.Equal(t, cliDescriptor.Source.SHA256, prepared.Source.SHA256)
	for _, exported := range []string{cliExport, libraryExport} {
		assertMaterialHashes(t, prepared.Current, exported)
	}

	cliRoundTrip := filepath.Join(root, "cli-round-trip")
	runCLI(t, binary, "prepare", "--recording", libraryExport, "--fixture-dir", cliRoundTrip)
	cliRoundTripExport := filepath.Join(root, "cli-round-trip-export")
	runCLI(t, binary, "export", "--fixture-dir", cliRoundTrip, "--output", cliRoundTripExport)
	assertEquivalentMaterials(t, libraryExport, cliRoundTripExport)
	assertMaterialHashes(t, prepared.Current, cliRoundTripExport)

	libraryRoundTrip := filepath.Join(root, "library-round-trip")
	roundTripped, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: libraryRoundTrip,
		Recording:  cliExport,
	})
	require.NoError(t, err)
	require.NotNil(t, roundTripped.Current)
	libraryRoundTripExport := filepath.Join(root, "library-round-trip-export")
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{
		FixtureDir: roundTripped.FixtureDir,
		Output:     libraryRoundTripExport,
	}))
	assertEquivalentMaterials(t, cliExport, libraryRoundTripExport)
	assertMaterialHashes(t, prepared.Current, libraryRoundTripExport)
}

func runCLI(t *testing.T, binary string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func sharedLayout(t *testing.T, root, materialID string) []string {
	t.Helper()
	var paths []string
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "runtime/tool.json" || rel == "runtime/launchers" || strings.HasPrefix(rel, "runtime/launchers/") {
			return nil
		}
		// Compare the location of the explicitly selected package, without
		// normalizing any material file bytes or protocol identities.
		if rel == "packages/"+materialID || strings.HasPrefix(rel, "packages/"+materialID+"/") {
			rel = "packages/<material>" + strings.TrimPrefix(rel, "packages/"+materialID)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		kind := "file"
		if info.IsDir() {
			kind = "dir"
		} else if info.Mode()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		paths = append(paths, kind+" "+rel)
		return nil
	}))
	sort.Strings(paths)
	return paths
}

var fixtureIdentityKeys = []string{"fixtureId", "tool", "transport", "command", "args", "launcher"}

func normalizedFixture(t *testing.T, fixtureDir string) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, readJSON(filepath.Join(fixtureDir, "fixture.json"), &value))
	return normalizeValue(value, fixtureDir).(map[string]any)
}

func assertFixtureToolSource(t *testing.T, fixtureDir, want string) {
	t.Helper()
	var descriptor struct {
		Tool struct {
			Source string `json:"source"`
		} `json:"tool"`
	}
	require.NoError(t, readJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor))
	require.Equal(t, want, descriptor.Tool.Source)
}

func assertPublicFixtureIdentity(t *testing.T, fixture *agenttestify.Fixture, source agenttestify.ToolSource) {
	t.Helper()
	require.Equal(t, source, fixture.Tool.Source)
	for _, action := range []string{"record", "mock"} {
		route := fixture.Routes[action]
		require.True(t, route.Enabled)
		require.NotEmpty(t, route.Connections)
		for _, connection := range route.Connections {
			switch source {
			case agenttestify.ToolSourceExecutable:
				require.Equal(t, agenttestify.ConnectionTransportProcess, connection.Transport)
				require.True(t, filepath.IsAbs(connection.Command))
			case agenttestify.ToolSourceEmbedded:
				require.Equal(t, agenttestify.ConnectionTransportInProcess, connection.Transport)
				require.Empty(t, connection.Command)
				require.Empty(t, connection.Args)
				require.Empty(t, connection.Launcher)
			}
		}
	}
	switch source {
	case agenttestify.ToolSourceExecutable:
		require.True(t, filepath.IsAbs(fixture.Tool.Command))
		require.NotEmpty(t, fixture.Tool.SHA256)
		require.Empty(t, fixture.Tool.Module)
	case agenttestify.ToolSourceEmbedded:
		require.Equal(t, libraryModule, fixture.Tool.Module)
		require.Empty(t, fixture.Tool.Command)
		require.Empty(t, fixture.Tool.Args)
		require.Empty(t, fixture.Tool.SHA256)
	}
}

func normalizeValue(value any, fixtureDir string) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			if slices.Contains(fixtureIdentityKeys, key) {
				continue
			}
			out[key] = normalizeValue(item, fixtureDir)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = normalizeValue(item, fixtureDir)
		}
		return out
	case string:
		if value == fixtureDir {
			return "<fixture>"
		}
		if strings.HasPrefix(value, fixtureDir+string(filepath.Separator)) {
			return filepath.ToSlash("<fixture>" + strings.TrimPrefix(value, fixtureDir))
		}
		return value
	default:
		return value
	}
}

func assertDescriptorSchemas(t *testing.T, cliFixture, libraryFixture string) {
	t.Helper()
	for _, relative := range []string{
		"executions/index.json",
		"packages/index.json",
		"current.json",
	} {
		var cliValue, libraryValue any
		require.NoError(t, readJSON(filepath.Join(cliFixture, relative), &cliValue))
		require.NoError(t, readJSON(filepath.Join(libraryFixture, relative), &libraryValue))
		require.EqualValues(t, 7, cliValue.(map[string]any)["schemaVersion"], relative)
		require.EqualValues(t, 7, libraryValue.(map[string]any)["schemaVersion"], relative)
		require.Equal(t, descriptorShape(cliValue), descriptorShape(libraryValue), relative)
	}
	var cliFixtureValue, libraryFixtureValue any
	require.NoError(t, readJSON(filepath.Join(cliFixture, "fixture.json"), &cliFixtureValue))
	require.NoError(t, readJSON(filepath.Join(libraryFixture, "fixture.json"), &libraryFixtureValue))
	require.EqualValues(t, 7, cliFixtureValue.(map[string]any)["schemaVersion"])
	require.EqualValues(t, 7, libraryFixtureValue.(map[string]any)["schemaVersion"])
	require.Equal(t, descriptorShape(cliFixtureValue, fixtureIdentityKeys...), descriptorShape(libraryFixtureValue, fixtureIdentityKeys...), "fixture.json")
}

func descriptorShape(value any, ignoredKeys ...string) any {
	ignored := make(map[string]bool, len(ignoredKeys))
	for _, key := range ignoredKeys {
		ignored[key] = true
	}
	var shape func(any) any
	shape = func(value any) any {
		switch value := value.(type) {
		case map[string]any:
			result := make(map[string]any, len(value))
			for key, item := range value {
				if ignored[key] {
					continue
				}
				result[key] = shape(item)
			}
			return result
		case []any:
			result := make([]any, len(value))
			for i, item := range value {
				result[i] = shape(item)
			}
			return result
		case string:
			return "string"
		case float64:
			return "number"
		case bool:
			return "boolean"
		case nil:
			return "null"
		default:
			return fmt.Sprintf("%T", value)
		}
	}
	return shape(value)
}

func assertEquivalentMaterials(t *testing.T, cliExport, libraryExport string) {
	t.Helper()
	require.Equal(t, payloadHashes(t, cliExport), payloadHashes(t, libraryExport))
	require.Equal(t, treeBytes(t, cliExport), treeBytes(t, libraryExport))
}

func assertMaterialHashes(t *testing.T, material *agenttestify.Material, root string) {
	t.Helper()
	hashes := payloadHashes(t, root)
	require.Equal(t, material.PlanSHA256, hashes["plan.json"])
	require.Equal(t, material.ManifestSHA256, hashes["manifest.json"])
	require.Equal(t, material.RecordingSHA256, hashes[filepath.ToSlash(filepath.Join("raw", "raw.jsonl"))])
}

func treeBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	values := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		values[filepath.ToSlash(rel)] = append([]byte(nil), mustRead(t, path)...)
		return nil
	}))
	return values
}

func payloadHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	for name, value := range treeBytes(t, root) {
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(value))
	}
	return hashes
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
