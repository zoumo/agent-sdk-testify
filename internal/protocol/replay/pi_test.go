package replay

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiRPCRebindsOnlyCommandIDs(t *testing.T) {
	frames := []string{
		`>{"type":"get_state","id":"same"}`,
		`>{"type":"get_entries","id":"next"}`,
		`<{"type":"extension_ui_request","id":"same","method":"confirm","message":"same"}`,
		`>{"type":"extension_ui_response","id":"same","confirmed":true}`,
		`<{"type":"response","id":"next","command":"get_entries","success":true,"data":{"lastEntryId":"next"}}`,
		`<{"type":"response","id":"same","command":"get_state","success":true,"data":{"sessionId":"same"}}`,
		`<{"type":"extension_ui_request","method":"notify","id":"notice","message":"same"}`,
	}
	input := strings.Join([]string{strings.Replace(frames[0][1:], "same", "live1", 1), strings.Replace(frames[1][1:], "next", "live2", 1), frames[3][1:]}, "\n") + "\n"
	out, consumed, err := exercise(t, "pi", frames, input, "")
	require.NoError(t, err)
	require.True(t, consumed)
	require.Equal(t, strings.Join([]string{frames[2][1:], strings.Replace(frames[4][1:], "next", "live2", 1), strings.Replace(frames[5][1:], "same", "live1", 1), frames[6][1:], ""}, "\n"), out)
}

func TestPiRPCRejectsInvalidContinuation(t *testing.T) {
	command := `>{"type":"get_state","id":"x"}`
	answer := `<{"type":"response","command":"get_state","id":"x","success":true}`
	for _, tc := range []struct {
		name   string
		frames []string
		input  string
	}{
		{"missing response", []string{command}, command[1:]},
		{"missing request", []string{answer}, ""},
		{"wrong error response command", []string{command, `<{"type":"response","command":"get_tree","id":"x","success":false,"error":"synthetic"}`}, command[1:]},
		{"wrong error response ID", []string{command, `<{"type":"response","command":"get_state","id":"y","success":false,"error":"synthetic"}`}, command[1:]},
		{"changed client fields", []string{command, answer}, strings.Replace(command[1:], "get_state", "get_tree", 1)},
		{"nonstring ID", []string{command, answer}, strings.Replace(command[1:], `"x"`, "1", 1)},
		{"unanswered UI dialog", []string{`<{"type":"extension_ui_request","id":"q","method":"input"}`}, ""},
		{"unmatched UI reply", []string{`>{"type":"extension_ui_response","id":"q","confirmed":false}`}, `{"type":"extension_ui_response","id":"q","confirmed":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, consumed, err := exercise(t, "pi", tc.frames, tc.input, "")
			require.Error(t, err)
			require.False(t, consumed)
		})
	}
}

func TestPiRPCRemapsBashEventsAndReplaysTimedDialog(t *testing.T) {
	frames := []string{
		`>{"type":"bash","id":"old","command":"synthetic","stream":true}`,
		`<{"type":"bash_execution_update","id":"old","output":"old"}`,
		`<{"type":"extension_ui_request","method":"input","id":"dialog","timeout":2}`,
		`<{"type":"response","id":"old","command":"bash","success":false,"error":"old"}`,
		`>{"type":"get_state"}`,
		`<{"type":"response","command":"get_state","success":true}`,
	}
	input := strings.Replace(frames[0][1:], "old", "live", 1) + "\n" + frames[4][1:] + "\n"
	out, consumed, err := exercise(t, "pi", frames, input, "")
	require.NoError(t, err)
	require.True(t, consumed)
	require.Equal(t, strings.Join([]string{
		strings.Replace(frames[1][1:], "old", "live", 1),
		frames[2][1:],
		strings.Replace(frames[3][1:], "old", "live", 1),
		frames[5][1:],
		"",
	}, "\n"), out)
}

func TestPiRPCAllowsIDReuseAfterResponse(t *testing.T) {
	frames := []string{
		`>{"type":"get_state","id":"same"}`,
		`<{"type":"response","command":"get_state","id":"same","success":true}`,
		`>{"type":"get_entries","id":"same"}`,
		`<{"type":"response","command":"get_entries","id":"same","success":true}`,
	}
	input := strings.ReplaceAll(frames[0][1:]+"\n"+frames[2][1:]+"\n", "same", "live")
	out, consumed, err := exercise(t, "pi", frames, input, "")
	require.NoError(t, err)
	require.True(t, consumed)
	require.Equal(t, strings.ReplaceAll(frames[1][1:]+"\n"+frames[3][1:]+"\n", "same", "live"), out)
}
