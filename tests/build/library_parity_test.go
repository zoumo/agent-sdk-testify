package build

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

func TestInstalledCLIAndLibraryShareSessionAndMaterialContract(t *testing.T) {
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

	cliSession := filepath.Join(root, "cli-session")
	runCLI(t, binary, "prepare", "--agent", "codex", "--case", "SDK-CASE-001", "--session-dir", cliSession)
	librarySession := filepath.Join(root, "library-session")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		SessionDir: librarySession,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	require.NotNil(t, prepared.Current)

	cliSession, err = filepath.EvalSymlinks(cliSession)
	require.NoError(t, err)
	require.Equal(t, sharedLayout(t, cliSession), sharedLayout(t, prepared.SessionDir))
	require.Equal(t, normalizedSession(t, cliSession), normalizedSession(t, prepared.SessionDir))
	assertDescriptorSchemas(t, cliSession, prepared.SessionDir)

	cliExport, libraryExport := filepath.Join(root, "cli-export"), filepath.Join(root, "library-export")
	runCLI(t, binary, "export", "--session-dir", cliSession, "--output", cliExport)
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{
		SessionDir: prepared.SessionDir,
		Output:     libraryExport,
	}))
	assertEquivalentMaterials(t, cliExport, libraryExport)

	var cliDescriptor struct {
		Source struct {
			SHA256 string `json:"sha256"`
		} `json:"source"`
	}
	require.NoError(t, readJSON(filepath.Join(cliSession, "session.json"), &cliDescriptor))
	require.Equal(t, cliDescriptor.Source.SHA256, prepared.Source.SHA256)
	for _, exported := range []string{cliExport, libraryExport} {
		assertMaterialHashes(t, prepared.Current, exported)
	}

	cliRoundTrip := filepath.Join(root, "cli-round-trip")
	runCLI(t, binary, "prepare", "--recording", libraryExport, "--session-dir", cliRoundTrip)
	cliRoundTripExport := filepath.Join(root, "cli-round-trip-export")
	runCLI(t, binary, "export", "--session-dir", cliRoundTrip, "--output", cliRoundTripExport)
	assertEquivalentMaterials(t, libraryExport, cliRoundTripExport)
	assertMaterialHashes(t, prepared.Current, cliRoundTripExport)

	libraryRoundTrip := filepath.Join(root, "library-round-trip")
	roundTripped, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		SessionDir: libraryRoundTrip,
		Recording:  cliExport,
	})
	require.NoError(t, err)
	require.NotNil(t, roundTripped.Current)
	libraryRoundTripExport := filepath.Join(root, "library-round-trip-export")
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{
		SessionDir: roundTripped.SessionDir,
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

func sharedLayout(t *testing.T, root string) []string {
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
		if rel == "." || rel == "runtime/launchers" || strings.HasPrefix(rel, "runtime/launchers/") {
			return nil
		}
		rel = normalizedLayoutPath(rel)
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

var materialDirectory = regexp.MustCompile(`^materials/[0-9a-f]{32}(/|$)`)

func normalizedLayoutPath(relative string) string {
	return materialDirectory.ReplaceAllString(relative, "materials/<material>$1")
}

func normalizedSession(t *testing.T, sessionDir string) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, readJSON(filepath.Join(sessionDir, "session.json"), &value))
	return normalizeValue(value, sessionDir).(map[string]any)
}

func normalizeValue(value any, sessionDir string) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			switch key {
			case "sessionId", "tool", "toolVersion", "toolSHA256", "toolSource", "command", "launcher", "embedded":
				continue
			}
			out[key] = normalizeValue(item, sessionDir)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = normalizeValue(item, sessionDir)
		}
		return out
	case string:
		if value == sessionDir {
			return "<session>"
		}
		if strings.HasPrefix(value, sessionDir+string(filepath.Separator)) {
			return filepath.ToSlash("<session>" + strings.TrimPrefix(value, sessionDir))
		}
		return value
	default:
		return value
	}
}

func assertDescriptorSchemas(t *testing.T, cliSession, librarySession string) {
	t.Helper()
	for _, relative := range []string{
		"executions/index.json",
		"materials/index.json",
		"current.json",
	} {
		var cliValue, libraryValue any
		require.NoError(t, readJSON(filepath.Join(cliSession, relative), &cliValue))
		require.NoError(t, readJSON(filepath.Join(librarySession, relative), &libraryValue))
		require.Equal(t, descriptorShape(cliValue), descriptorShape(libraryValue), relative)
	}
	var cliSessionValue, librarySessionValue any
	require.NoError(t, readJSON(filepath.Join(cliSession, "session.json"), &cliSessionValue))
	require.NoError(t, readJSON(filepath.Join(librarySession, "session.json"), &librarySessionValue))
	ignored := []string{"sessionId", "tool", "toolVersion", "toolSHA256", "toolSource", "command", "launcher", "embedded"}
	require.Equal(t, descriptorShape(cliSessionValue, ignored...), descriptorShape(librarySessionValue, ignored...), "session.json")
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
	cliHashes, libraryHashes := payloadHashes(t, cliExport), payloadHashes(t, libraryExport)
	delete(cliHashes, "material.json")
	delete(libraryHashes, "material.json")
	require.Equal(t, cliHashes, libraryHashes)
	cliFiles, libraryFiles := treeBytes(t, cliExport), treeBytes(t, libraryExport)
	require.Equal(t, sortedKeys(cliFiles), sortedKeys(libraryFiles))
	for name, cliBytes := range cliFiles {
		libraryBytes := libraryFiles[name]
		if name == "material.json" {
			assertEquivalentMaterialMetadata(t, cliBytes, libraryBytes)
			continue
		}
		require.Equal(t, cliBytes, libraryBytes, name)
	}
}

func assertMaterialHashes(t *testing.T, material *agenttestify.Material, root string) {
	t.Helper()
	hashes := payloadHashes(t, root)
	require.Equal(t, material.PlanSHA256, hashes["plan.json"])
	require.Equal(t, material.ManifestSHA256, hashes["manifest.json"])
	require.Equal(t, material.RecordingSHA256, hashes[filepath.ToSlash(filepath.Join("raw", "raw.jsonl"))])
}

func assertEquivalentMaterialMetadata(t *testing.T, cliBytes, libraryBytes []byte) {
	t.Helper()
	var cliValue, libraryValue map[string]any
	require.NoError(t, json.Unmarshal(cliBytes, &cliValue))
	require.NoError(t, json.Unmarshal(libraryBytes, &libraryValue))
	for _, key := range []string{"id", "path", "createdAt"} {
		delete(cliValue, key)
		delete(libraryValue, key)
	}
	require.Equal(t, cliValue, libraryValue)
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

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
