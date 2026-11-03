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

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func TestBuiltinCatalogOutsideCheckout(t *testing.T) {
	data, err := mockCommand(t, "--list", "--json").CombinedOutput()
	require.NoError(t, err, "%s", data)
	var listing object
	require.NoError(t, json.Unmarshal(data, &listing))
	assert.InDelta(t, float64(1), listing["schemaVersion"], 0)
	rows := array(listing["cases"])
	require.Len(t, rows, 72)
	available := 0
	for _, value := range rows {
		row := obj(value)
		assert.Contains(t, []string{"required", "optional"}, row["requirement"])
		assert.NotEmpty(t, row["contract"])
		assert.NotEmpty(t, row["binding"])
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
	data, err = mockCommand(t, "--describe", "--agent", "codex", "--case", "SDK-CASE-001", "--json").CombinedOutput()
	require.NoError(t, err, "%s", data)
	var description object
	require.NoError(t, json.Unmarshal(data, &description))
	assert.Equal(t, "SDK-CASE-001", description["caseId"])
	assert.NotEmpty(t, obj(description["material"])["files"])
	assert.NotEmpty(t, obj(description["material"])["officialSDK"])
	assert.NotEmpty(t, description["plan"])
}

// Byte transcripts are transport probes, not official SDK/business verification.
func builtinTranscript(t *testing.T, provider, id string) (string, string) {
	t.Helper()
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	var index object
	require.NoError(t, readJSON(filepath.Join(repository, "data", "index.json"), &index))
	for _, value := range array(index["cases"]) {
		row := obj(value)
		if row["provider"] != provider || row["caseId"] != id {
			continue
		}
		root := filepath.Join(repository, "data", str(row["materials"]))
		data, err := os.ReadFile(filepath.Join(root, "raw", "raw.jsonl"))
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
			root, err := filepath.Abs(root)
			require.NoError(t, err)
			var good []byte
			for _, mode := range []string{"builtin", "external"} {
				for _, valid := range []bool{true, false} {
					t.Run(fmt.Sprintf("%s/%t", mode, valid), func(t *testing.T) {
						report := filepath.Join(t.TempDir(), "receipt.json")
						args := []string{"--agent", provider, "--report", report}
						if mode == "builtin" {
							args = append(args, "--case", "SDK-CASE-001")
						} else {
							args = append(args, "--recording", root)
						}
						cmd := mockCommand(t, args...)
						if valid {
							cmd.Stdin = strings.NewReader(input)
						} else {
							cmd.Stdin = strings.NewReader("{\"id\":1,\"method\":\"wrong-first-frame\"}\n")
						}
						var stderr bytes.Buffer
						cmd.Stderr = &stderr
						out, err := cmd.Output()
						var receipt object
						require.NoError(t, readJSON(report, &receipt))
						assert.Equal(t, true, receipt["cleanupComplete"])
						if valid {
							require.NoError(t, err, "%s", stderr.String())
							assert.Equal(t, "passed", receipt["status"])
							if mode == "builtin" {
								good = out
								assert.Equal(t, "builtin", receipt["provenance"])
								assert.NoDirExists(t, str(obj(receipt["session"])["resourceRoot"]))
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

func TestBuiltinManagedAliasUsesPreparedSession(t *testing.T) {
	input, _ := builtinTranscript(t, "codex", "SDK-CASE-001")
	report := filepath.Join(t.TempDir(), "receipt.json")
	cmd := mockCommand(t, "--agent", "codex", "--case", "SDK-CASE-001", "--report", report)
	cmd.Stdin = strings.NewReader(input)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var receipt object
	require.NoError(t, readJSON(report, &receipt))
	assert.Equal(t, "passed", receipt["status"])
	assert.Equal(t, "builtin", receipt["provenance"])
	assert.NotEmpty(t, receipt["sessionId"])
	assert.NotEmpty(t, receipt["executionId"])
	assert.Equal(t, "passed", obj(receipt["finish"])["status"])
	assert.NoDirExists(t, str(obj(receipt["session"])["resourceRoot"]))
}

func TestBuiltinMissingAndUnsupported(t *testing.T) {
	for _, test := range []struct{ provider, id, reason string }{
		{"codex", "SDK-CASE-003", "unsupported"},
		{"codex", "SDK-CASE-012", "blocked"},
		{"claude", "SDK-CASE-003", "failed"},
		{"claude", "SDK-CASE-026", "unverified"},
		{"codex", "SDK-CASE-999", "unknown"},
	} {
		t.Run(test.provider+test.id, func(t *testing.T) {
			data, err := mockCommand(t, "--agent", test.provider, "--case", test.id, "--report", filepath.Join(t.TempDir(), "receipt.json")).CombinedOutput()
			require.Error(t, err)
			assert.Contains(t, string(data), test.reason)
		})
	}
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
	require.NoError(t, copyTree(filepath.Join(repository, "data"), filepath.Join(root, "data")))
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
	var empty object
	require.NoError(t, readJSON(indexPath, &empty))
	for _, value := range array(empty["cases"]) {
		row := obj(value)
		row["verification"] = "unverified"
		row["verificationReason"] = "no selected official evidence in this test"
	}
	require.NoError(t, writeJSON(indexPath, empty))
	data, err = invoke()
	require.Error(t, err)
	assert.Contains(t, string(data), "empty qualified builtin set")
}

func TestBuiltinDescribeAssertionInputsPreserveNativeBytes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			data, err := mockCommand(t, "--describe", "--agent", provider, "--case", "SDK-CASE-018", "--json").Output()
			require.NoError(t, err)
			var description struct {
				AssertionInputs struct {
					SchemaVersion int `json:"schemaVersion"`
					Connections   []struct {
						ID       string            `json:"id"`
						SHA256   string            `json:"sha256"`
						Original []byte            `json:"originalBase64"`
						Frames   []json.RawMessage `json:"frames"`
					} `json:"connections"`
					SourceReport struct {
						SHA256   string          `json:"sha256"`
						Original []byte          `json:"originalBase64"`
						Value    json.RawMessage `json:"value"`
					} `json:"sourceReport"`
				} `json:"assertionInputs"`
			}
			require.NoError(t, json.Unmarshal(data, &description))
			inputs := description.AssertionInputs
			require.Equal(t, 1, inputs.SchemaVersion)
			require.Len(t, inputs.Connections, 1)
			c := inputs.Connections[0]
			assert.Equal(t, "main", c.ID)
			_, source := builtinTranscript(t, provider, "SDK-CASE-018")
			raw, err := os.ReadFile(filepath.Join(source, "raw", "raw.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, raw, c.Original)
			assert.Equal(t, digestBytes(raw), c.SHA256)
			lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
			require.Len(t, c.Frames, len(lines))
			// Compact only JSON whitespace; protected native values/number lexemes remain exact.
			for i, line := range lines {
				var compact bytes.Buffer
				require.NoError(t, json.Compact(&compact, line))
				assert.Equal(t, compact.String(), string(c.Frames[i]))
			}
			report, err := os.ReadFile(filepath.Join(source, "report.json"))
			require.NoError(t, err)
			assert.Equal(t, report, inputs.SourceReport.Original)
			assert.Equal(t, digestBytes(report), inputs.SourceReport.SHA256)
			var compact bytes.Buffer
			require.NoError(t, json.Compact(&compact, report))
			assert.Equal(t, compact.String(), string(inputs.SourceReport.Value))
		})
	}
}

func TestExternalSyntheticDescribeRetainsUnqualifiedNativeInputs(t *testing.T) {
	root := mockCapture(t)
	// Independent synthetic values exercise number lexemes beyond float64 and HTML.
	plan := []byte(`{"provider":"codex","caseId":"sdk-request","provenance":"synthetic","unknown":9007199254740993}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), plan, 0o600))
	var manifest object
	require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
	manifest["planSHA256"] = digestBytes(plan)
	report := []byte(`{"status":"passed","protected":{"unknown":9007199254740993,"decimal":1.00,"html":"<native>"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, "report.json"), report, 0o600))
	require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), manifest))
	data, err := mockCommand(t, "--describe", "--recording", root, "--json").CombinedOutput()
	require.NoError(t, err, "%s", data)
	var scene struct {
		SchemaVersion   int    `json:"schemaVersion"`
		Available       bool   `json:"available"`
		Qualification   string `json:"qualification"`
		Provenance      string `json:"provenance"`
		CaseID          string `json:"caseId"`
		AssertionInputs struct {
			SourcePlan   nativeDocument     `json:"sourcePlan"`
			SourceReport nativeDocument     `json:"sourceReport"`
			Connections  []nativeConnection `json:"connections"`
		} `json:"assertionInputs"`
	}
	require.NoError(t, json.Unmarshal(data, &scene))
	assert.Equal(t, 1, scene.SchemaVersion)
	assert.Equal(t, "unqualified", scene.Qualification)
	assert.Equal(t, "synthetic", scene.Provenance)
	assert.Equal(t, "sdk-request", scene.CaseID)
	assert.Equal(t, plan, scene.AssertionInputs.SourcePlan.Original)
	assert.Equal(t, report, scene.AssertionInputs.SourceReport.Original)
	assert.Contains(t, string(scene.AssertionInputs.SourceReport.Value), "9007199254740993")
	assert.Contains(t, string(scene.AssertionInputs.SourceReport.Value), "1.00")
	assert.Contains(t, string(scene.AssertionInputs.SourceReport.Value), "<native>")
	assert.Equal(t, digestBytes(report), scene.AssertionInputs.SourceReport.SHA256)
	assert.Contains(t, string(scene.AssertionInputs.Connections[0].Frames[0]), "9007199254740993")
	require.Len(t, scene.AssertionInputs.Connections, 1)
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte("drift"), 0o600))
	data, err = mockCommand(t, "--describe", "--recording", root, "--json").CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "hash")
}
