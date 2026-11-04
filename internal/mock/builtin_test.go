package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestBuiltinCatalogCoverage(t *testing.T) {
	manifest, _, err := builtin.Read()
	require.NoError(t, err)
	require.Len(t, manifest.Coverage, 72)
	available := 0
	for _, row := range manifest.Coverage {
		assert.Contains(t, []string{"required", "optional"}, row["requirement"])
		if row["available"] == true {
			available++
		} else {
			assert.NotEmpty(t, row["missingReason"])
		}
		if row["provider"] == "claude" && row["caseId"] == "SDK-CASE-003" {
			assert.Equal(t, "failed", row["verification"])
			assert.Equal(t, false, row["available"])
		}
	}
	assert.Equal(t, 64, available)
}

// Byte transcripts are transport probes, not official SDK/business verification.
func builtinTranscript(t *testing.T, provider, id string) (string, string) {
	return builtinConnectionTranscript(t, provider, id, "main")
}

func builtinConnectionTranscript(t *testing.T, provider, id, connection string) (string, string) {
	t.Helper()
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(repository, "data", "index.json"), &index))
	for _, value := range tool.Array(index["cases"]) {
		row := tool.Obj(value)
		if row["provider"] != provider || row["caseId"] != id {
			continue
		}
		root := filepath.Join(repository, "data", tool.Str(row["materials"]))
		rawRoot := filepath.Join(root, "raw")
		if connection == "probe" {
			rawRoot = filepath.Join(root, "remote-probe", "raw")
		} else if connection != "main" {
			t.Fatalf("unknown builtin connection %q", connection)
		}
		data, err := os.ReadFile(filepath.Join(rawRoot, "raw.jsonl"))
		require.NoError(t, err)
		var input strings.Builder
		for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
			var frame map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(line, &frame))
			if string(frame["direction"]) == `"c2s"` {
				input.Write(frame["wire"])
				input.WriteByte(10)
			}
		}
		return input.String(), root
	}
	t.Fatal("missing test capture")
	return "", ""
}

func TestBuiltinAndExternalStrictReplay(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			input, root := builtinTranscript(t, provider, "SDK-CASE-001")
			var good []byte
			for _, mode := range []string{"builtin", "external"} {
				for _, valid := range []bool{true, false} {
					t.Run(fmt.Sprintf("%s/%t", mode, valid), func(t *testing.T) {
						args := []string{"prepare", "--recording", root}
						if mode == "builtin" {
							args = []string{"prepare", "--agent", provider, "--case", "SDK-CASE-001"}
						}
						session, env := preparedSessionWithArgs(t, args...)
						cmd := sessionTool(t, env, "mock", "--session-dir", session)
						if valid {
							cmd.Stdin = strings.NewReader(input)
						} else {
							cmd.Stdin = strings.NewReader("{\"id\":1,\"method\":\"wrong-first-frame\"}\n")
						}
						var stderr bytes.Buffer
						cmd.Stderr = &stderr
						out, err := cmd.Output()
						receipt := mockReceipt(t, session)
						assert.Equal(t, true, receipt["cleanupComplete"])
						if valid {
							require.NoError(t, err, "%s", stderr.String())
							assert.Equal(t, "passed", receipt["status"])
							if mode == "builtin" {
								good = out
								execution, _ := latestSessionExecution(t, session, "mock")
								assert.Equal(t, "builtin", execution["provenance"])
							} else {
								assert.Equal(t, string(good), string(out))
							}
						} else {
							require.Error(t, err)
							assert.Equal(t, "failed", receipt["status"])
							assert.Equal(t, false, receipt["fullConsumption"])
						}
					})
				}
			}
		})
	}
}

func TestBuiltinMissingAndUnsupported(t *testing.T) {
	for _, test := range []struct{ provider, id string }{
		{"codex", "SDK-CASE-003"}, {"codex", "SDK-CASE-012"}, {"claude", "SDK-CASE-003"}, {"claude", "SDK-CASE-026"},
	} {
		t.Run(test.provider+test.id, func(t *testing.T) {
			session, env := preparedSessionWithArgs(t, "prepare", "--agent", test.provider, "--case", test.id)
			data, err := sessionTool(t, env, "mock", "--session-dir", session).CombinedOutput()
			require.Error(t, err)
			assert.Contains(t, string(data), "no current recording")
		})
	}
	session := filepath.Join(t.TempDir(), "session")
	data, err := sessionTool(t, os.Environ(), "prepare", "--session-dir", session, "--agent", "codex", "--case", "SDK-CASE-999").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "absent from shared materials")
}

func TestBuiltinGeneratorArtifactContract(t *testing.T) {
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	generator := filepath.Join(t.TempDir(), "generator")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-tags", "builtin_generate", "-o", generator, ".")
	cmd.Dir = repository
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	// Check recomputes the entire closure rather than trusting generated hashes.
	cmd = exec.CommandContext(t.Context(), generator, "--check")
	cmd.Dir = repository
	data, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	root := t.TempDir()
	require.NoError(t, tool.CopyTree(filepath.Join(repository, "data"), filepath.Join(root, "data")))
	invoke := func() ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), generator, "--root", root, "--check")
		cmd.Dir = repository
		return cmd.CombinedOutput()
	}
	// A selected missing file must fail, even though the generated bundle exists.
	_, source := builtinTranscript(t, "codex", "SDK-CASE-001")
	relativeSource, err := filepath.Rel(repository, source)
	require.NoError(t, err)
	raw := filepath.Join(root, relativeSource, "raw", "raw.jsonl")
	original, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.NoError(t, os.Remove(raw))
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "inventory drift")
	require.NoError(t, os.WriteFile(raw, append(original, 10), 0o600))
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "inventory drift")
	require.NoError(t, os.WriteFile(raw, original, 0o600))
	// Changes in the authoritative contract invalidate the generated snapshot.
	contract := filepath.Join(root, "data", "scenarios", "SDK-US-001.json")
	f, err := os.OpenFile(contract, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "builtin drift")
	indexPath := filepath.Join(root, "data", "index.json")
	var empty tool.Object
	require.NoError(t, tool.ReadJSON(indexPath, &empty))
	for _, value := range tool.Array(empty["cases"]) {
		row := tool.Obj(value)
		row["verification"] = "unverified"
		row["verificationReason"] = "no selected official evidence in this test"
	}
	require.NoError(t, tool.WriteJSON(indexPath, empty))
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "empty qualified builtin set")
}

// Removed query envelopes are replaced by the session's lossless material copy.
func TestPreparedMaterialsPreserveNativeBytes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			_, source := builtinTranscript(t, provider, "SDK-CASE-018")
			session, _ := preparedSessionWithArgs(t, "prepare", "--agent", provider, "--case", "SDK-CASE-018")
			// The registry owns the immutable current material.
			var index struct {
				Materials []struct {
					Path string `json:"path"`
				} `json:"materials"`
			}
			require.NoError(t, tool.ReadJSON(filepath.Join(session, "materials", "index.json"), &index))
			require.Len(t, index.Materials, 1)
			material := index.Materials[0].Path
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
	session, _ := preparedSessionWithArgs(t, "prepare", "--recording", root)
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "session.json"), &descriptor))
	material := tool.Str(tool.Obj(descriptor["source"])["path"])
	copied, err := os.ReadFile(filepath.Join(material, "report.json"))
	require.NoError(t, err)
	assert.Equal(t, report, copied)
	copied, err = os.ReadFile(filepath.Join(material, "plan.json"))
	require.NoError(t, err)
	assert.Equal(t, plan, copied)
}
