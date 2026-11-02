package recording_test

import (
	"bytes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/rawjson"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
	"io"
	"strings"
	"testing"
	"time"
)

// These are deliberately synthetic format examples, not copies of the SDK corpus.
const native = `{ "future":{"z":1e+09,"huge":9007199254740993,"fraction":0.123456789012345678901234567890},"type":"notice" }`
const unified = `{"ordinal":1,"direction":"s2c","channel":"protocol","epoch":1,"elapsedMs":12,"wire":` + native + `}`
const legacy = `{"ordinal":1,"direction":"server","channel":"control","data":` + native + `}`

func TestHistoricalFormatsPreserveRaw(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for name, input := range map[string]string{
			"unified": unified + "\n", "legacy": legacy + "\r\n", "unterminated historical file": unified,
			"unified scenario": `{"version":1,"provenance":"synthetic","timeoutMs":125,"frames":[` + unified + `]}`,
			"legacy scenario":  `{"version":1,"provenance":"recording","source":{"path":"old/fixture","sha256":"unchanged"},"frames":[` + legacy + `]}`,
		} {
			t.Run(provider+"/"+name, func(t *testing.T) {
				d, err := recording.Read(strings.NewReader(input), provider, recording.Limits{})
				require.NoError(t, err)
				require.Len(t, d.Frames, 1)
				assert.Equal(t, native, string(d.Frames[0].Wire.Raw()))
				assert.Equal(t, []byte(input), d.Original)
				assert.Equal(t, recording.Hash([]byte(input)), d.SHA256)
				assert.Equal(t, recording.ServerToClient, d.Frames[0].Direction)
				assert.Equal(t, recording.CurrentFormat(), d.Format)
				if name == "unified scenario" {
					assert.Equal(t, 125*time.Millisecond, d.Timeout)
				}
				if name == "legacy scenario" {
					assert.Equal(t, `{"path":"old/fixture","sha256":"unchanged"}`, string(d.Source.Raw()))
				}
			})
		}
	}
}

func TestEncodePreservesWireAndRejectsNonCaptureFrames(t *testing.T) {
	wire, err := rawjson.Parse([]byte("  " + native + " "))
	require.NoError(t, err)
	f := recording.Frame{Ordinal: 1, Direction: recording.ClientToServer, Channel: "protocol", Epoch: 1, Wire: wire}
	data, err := recording.EncodeFrame(f)
	require.NoError(t, err)
	assert.True(t, bytes.Contains(data, append([]byte(`"wire":`), wire.Raw()...)))
	d, err := recording.Read(bytes.NewReader(data), "codex", recording.Limits{})
	require.NoError(t, err)
	assert.Equal(t, native, string(d.Frames[0].Wire.Raw()))
	f.Disconnect = true
	_, err = recording.EncodeFrame(f)
	require.Error(t, err)
	f.Disconnect = false
	f.Wire, err = rawjson.Parse([]byte("{\n}"))
	require.NoError(t, err)
	_, err = recording.EncodeFrame(f)
	require.Error(t, err)
}

func TestSyntheticFaults(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, last := range []string{`{"ordinal":2,"direction":"s2c","channel":"protocol","epoch":1,"disconnect":true,"wire":null}`, `{"ordinal":2,"direction":"disconnect","channel":"control"}`} {
			input := `{"version":1,"provenance":"synthetic","frames":[` + strings.Replace(unified, `"wire":`, `"delayMs":1,"wire":`, 1) + `,` + last + `]}`
			d, err := recording.Read(strings.NewReader(input), provider, recording.Limits{})
			require.NoError(t, err)
			assert.Equal(t, time.Millisecond, d.Frames[0].Delay)
			assert.True(t, d.Frames[1].Disconnect)
			assert.Equal(t, rawjson.Invalid, d.Frames[1].Wire.Kind())
		}
	}
}

func TestRejectsInvalidRecording(t *testing.T) {
	scenario := func(frames string) string { return `{"version":1,"provenance":"synthetic","frames":[` + frames + `]}` }
	cases := map[string]string{
		"empty": "", "blank line": unified + "\n\n", "truncated": unified[:len(unified)-2], "trailing": unified + " {}",
		"bad version":       `{"version":2,"provenance":"synthetic","frames":[` + unified + `]}`,
		"missing version":   `{"provenance":"synthetic","frames":[` + unified + `]}`,
		"duplicate version": `{"version":1,"version":1,"frames":[` + unified + `]}`,
		"fraction version":  `{"version":1.0,"provenance":"synthetic","frames":[` + unified + `]}`,
		"unknown wrapper":   `{"version":1,"provenance":"synthetic","extra":1,"frames":[` + unified + `]}`,
		"bad provenance":    `{"version":1,"provenance":"real","frames":[` + unified + `]}`,
		"source required":   `{"version":1,"provenance":"recording","frames":[` + unified + `]}`,
		"source null":       `{"version":1,"provenance":"recording","source":null,"frames":[` + unified + `]}`,
		"empty frames":      scenario(""), "wrong frames": `{"version":1,"provenance":"synthetic","frames":{}}`,
		"timeout":                 `{"version":1,"provenance":"synthetic","timeoutMs":300001,"frames":[` + unified + `]}`,
		"negative timeout":        `{"version":1,"provenance":"synthetic","timeoutMs":-1,"frames":[` + unified + `]}`,
		"unknown envelope":        strings.Replace(unified, `"epoch":1`, `"epoch":1,"new":true`, 1),
		"bad epoch":               strings.Replace(unified, `"epoch":1`, `"epoch":2`, 1),
		"missing epoch":           strings.Replace(unified, `"epoch":1,`, ``, 1),
		"bad ordinal":             strings.Replace(unified, `"ordinal":1`, `"ordinal":0`, 1),
		"fraction ordinal":        strings.Replace(unified, `"ordinal":1`, `"ordinal":1.0`, 1),
		"elapsed negative":        strings.Replace(unified, `"elapsedMs":12`, `"elapsedMs":-1`, 1),
		"elapsed wrong type":      strings.Replace(unified, `"elapsedMs":12`, `"elapsedMs":"1"`, 1),
		"bad channel":             strings.Replace(unified, `"protocol"`, `"stdout"`, 1),
		"bad direction":           strings.Replace(unified, `"s2c"`, `"unknown"`, 1),
		"duplicate wire key":      strings.Replace(unified, native, `{"x":1,"x":2}`, 1),
		"scalar wire":             strings.Replace(unified, native, `true`, 1),
		"negative delay":          scenario(strings.Replace(unified, `"wire":`, `"delayMs":-1,"wire":`, 1)),
		"excess delay":            scenario(strings.Replace(unified, `"wire":`, `"delayMs":300001,"wire":`, 1)),
		"recording delay":         strings.Replace(unified, `"wire":`, `"delayMs":1,"wire":`, 1),
		"bad disconnect type":     scenario(strings.Replace(unified, `"wire":`, `"disconnect":1,"wire":`, 1)),
		"disconnect with payload": scenario(strings.Replace(unified, `"wire":`, `"disconnect":true,"wire":`, 1)),
		"disconnect client":       scenario(`{"ordinal":1,"direction":"c2s","channel":"protocol","epoch":1,"wire":null,"disconnect":true}`),
		"disconnect not final":    scenario(`{"ordinal":1,"direction":"disconnect","channel":"protocol"},` + strings.Replace(unified, `"ordinal":1`, `"ordinal":2`, 1)),
		"recording disconnect":    `{"ordinal":1,"direction":"disconnect","channel":"protocol"}`,
		"duplicate ordinal":       unified + "\n" + unified,
	}
	for _, provider := range []string{"codex", "claude"} {
		for name, input := range cases {
			t.Run(provider+"/"+name, func(t *testing.T) {
				_, err := recording.Read(strings.NewReader(input), provider, recording.Limits{})
				require.Error(t, err)
			})
		}
	}
}

func TestBoundsAndProviderOrder(t *testing.T) {
	for _, tt := range []struct {
		name, provider, input string
		limits                recording.Limits
		fail                  bool
	}{
		{"unknown provider", "unknown", unified, recording.Limits{}, true},
		{"negative limit", "codex", unified, recording.Limits{MaxFrames: -1}, true},
		{"too many configured", "codex", unified, recording.Limits{MaxFrames: 10001}, true},
		{"source limit", "codex", unified, recording.Limits{MaxBytes: 8}, true},
		{"source exact", "codex", unified, recording.Limits{MaxBytes: int64(len(unified))}, false},
		{"frame exact excluded", "codex", unified, recording.Limits{MaxFrameBytes: int64(len(native))}, true},
		{"frame below bound", "codex", unified, recording.Limits{MaxFrameBytes: int64(len(native) + 1)}, false},
		{"count", "codex", unified + "\n" + strings.Replace(unified, `"ordinal":1`, `"ordinal":2`, 1), recording.Limits{MaxFrames: 1}, true},
		{"codex gaps", "codex", strings.Replace(unified, `"ordinal":1`, `"ordinal":7`, 1), recording.Limits{}, false},
		{"claude consecutive", "claude", strings.Replace(unified, `"ordinal":1`, `"ordinal":7`, 1), recording.Limits{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := recording.Read(strings.NewReader(tt.input), tt.provider, tt.limits)
			if tt.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	large := strings.Replace(unified, native, `{"text":"`+strings.Repeat("x", 128<<10)+`"}`, 1)
	d, err := recording.Read(strings.NewReader(large), "codex", recording.Limits{})
	require.NoError(t, err)
	assert.Greater(t, len(d.Frames[0].Wire.Raw()), 64<<10)
	_, err = recording.Read(errorReader{}, "codex", recording.Limits{})
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestManifestValidationPreservesIdentity(t *testing.T) {
	content := []byte(unified + "\n")
	manifest := `{"recordingFormat":{"name":"agent-mock-jsonl","version":1,"transport":"stdio-jsonl"},"source":{"path":"original/unchanged","number":9007199254740993},"files":{"raw.jsonl":"` + recording.Hash(content) + `"}}`
	v, err := recording.ValidateManifest([]byte(manifest), "raw.jsonl", content)
	require.NoError(t, err)
	assert.Equal(t, manifest, string(v.Raw()))
	for _, data := range []string{strings.Replace(manifest, `"version":1`, `"version":2`, 1), strings.Replace(manifest, `stdio-jsonl`, `unknown`, 1), strings.Replace(manifest, `agent-mock-jsonl`, `unknown`, 1), `{}`, strings.Replace(manifest, `"source":`, `"source":null,"source":`, 1)} {
		_, err = recording.ValidateManifest([]byte(data), "raw.jsonl", content)
		require.Error(t, err)
	}
	_, err = recording.ValidateManifest([]byte(manifest), "raw.jsonl", []byte("changed"))
	require.Error(t, err)
	_, err = recording.ValidateManifest([]byte(manifest), "missing", content)
	require.Error(t, err)
	data, err := recording.EncodeReport(recording.Receipt{Version: 1, Provider: "codex", NativeLaunched: false})
	require.NoError(t, err)
	assert.True(t, bytes.HasSuffix(data, []byte{'\n'}))
	_, err = recording.EncodeReport(make(chan int))
	require.Error(t, err)
	assert.NotEqual(t, recording.Hash(nil), recording.Hash([]byte("x")))
}
