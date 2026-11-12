package mock

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Byte transcripts are transport probes, not official SDK/business verification.
func TestBuiltinAndExternalStrictReplay(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			input, root := builtinTranscript(t, provider, "SDK-CASE-001")
			var good []byte
			for _, mode := range []string{"builtin", "external"} {
				t.Run(mode, func(t *testing.T) {
					args := []string{"prepare", "--recording", root}
					if mode == "builtin" {
						args = []string{"prepare", "--agent", provider, "--case", "SDK-CASE-001"}
					}
					fixtureDir, env := preparedFixtureWithArgs(t, args...)
					cmd := fixtureTool(t, env, "agent", "mock", "--fixture-dir", fixtureDir)
					cmd.Stdin = strings.NewReader(input)
					var stderr bytes.Buffer
					cmd.Stderr = &stderr
					out, err := cmd.Output()
					require.NoError(t, err, "%s", stderr.String())
					receipt := mockReceipt(t, fixtureDir)
					assert.Equal(t, true, receipt["cleanupComplete"])
					assert.Equal(t, "passed", receipt["status"])
					if mode == "builtin" {
						good = out
						execution, _ := latestFixtureExecution(t, fixtureDir, "mock")
						assert.Equal(t, "builtin", execution["provenance"])
					} else {
						assert.Equal(t, string(good), string(out))
					}
				})
			}
		})
	}
}

func TestBuiltinMissingAndUnsupported(t *testing.T) {
	for _, test := range []struct{ provider, id string }{
		{"codex", "SDK-CASE-003"}, {"codex", "SDK-CASE-012"}, {"claude", "SDK-CASE-003"}, {"claude", "SDK-CASE-026"},
	} {
		t.Run(test.provider+test.id, func(t *testing.T) {
			fixtureDir, env := preparedFixtureWithArgs(t, "prepare", "--agent", test.provider, "--case", test.id)
			data, err := fixtureTool(t, env, "agent", "mock", "--fixture-dir", fixtureDir).CombinedOutput()
			require.Error(t, err)
			assert.Contains(t, string(data), "no current recording")
		})
	}
	fixtureDir := filepath.Join(t.TempDir(), "fixture")
	data, err := fixtureTool(t, os.Environ(), "prepare", "--fixture-dir", fixtureDir, "--agent", "codex", "--case", "SDK-CASE-999").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "absent from shared materials")
}

// Removed query envelopes are replaced by the fixture's lossless material copy.
func TestPreparedMaterialsPreserveNativeBytes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			_, source := builtinTranscript(t, provider, "SDK-CASE-018")
			fixtureDir, _ := preparedFixtureWithArgs(t, "prepare", "--agent", provider, "--case", "SDK-CASE-018")
			// The registry owns the immutable current material.
			var index struct {
				Materials []struct {
					ID   string `json:"id"`
					Path string `json:"path"`
				} `json:"materials"`
			}
			require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "materials", "index.json"), &index))
			require.Len(t, index.Materials, 1)
			require.Empty(t, index.Materials[0].Path)
			material := filepath.Join(fixtureDir, "materials", index.Materials[0].ID)
			for _, name := range []string{"plan.json", "report.json", "raw/raw.jsonl"} {
				original, err := os.ReadFile(filepath.Join(source, name))
				require.NoError(t, err)
				copied, err := os.ReadFile(filepath.Join(material, name))
				require.NoError(t, err)
				assert.Equal(t, original, copied)
				assert.Equal(t, tool.DigestBytes(original), tool.DigestBytes(copied))
			}
		})
	}
	root := mockCapture(t)
	plan := []byte(`{"provider":"codex","caseId":"sdk-request","provenance":"synthetic","unknown":9007199254740993}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), plan, 0o600))
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest))
	manifest["planSHA256"] = tool.DigestBytes(plan)
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), manifest))
	report := []byte(`{"status":"passed","protected":{"unknown":9007199254740993,"decimal":1.00,"html":"<native>"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, "report.json"), report, 0o600))
	fixtureDir, _ := preparedFixtureWithArgs(t, "prepare", "--recording", root)
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor))
	material := filepath.Join(fixtureDir, filepath.FromSlash(tool.Str(tool.Obj(descriptor["source"])["path"])))
	copied, err := os.ReadFile(filepath.Join(material, "report.json"))
	require.NoError(t, err)
	assert.Equal(t, report, copied)
	copied, err = os.ReadFile(filepath.Join(material, "plan.json"))
	require.NoError(t, err)
	assert.Equal(t, plan, copied)
}

func builtinTranscript(t *testing.T, provider, id string) (string, string) {
	return testutil.BuiltinTranscript(t, provider, id, "main")
}

func builtinConnectionTranscript(t *testing.T, provider, id, connection string) (string, string) {
	return testutil.BuiltinTranscript(t, provider, id, connection)
}
