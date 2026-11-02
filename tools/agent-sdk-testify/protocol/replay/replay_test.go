package replay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

func synthetic(t *testing.T, provider string, actions ...string) *recording.Document {
	t.Helper()
	var rows []string
	for i, action := range actions {
		direction := "c2s"
		if action[0] != '>' {
			direction = "s2c"
		}
		payload := `"wire":` + action[1:]
		if action[0] == '!' {
			payload = `"disconnect":true`
		}
		rows = append(rows, fmt.Sprintf(`{"ordinal":%d,"direction":%q,"channel":"protocol","epoch":1,%s}`, i+1, direction, payload))
	}
	d, err := recording.Read(strings.NewReader(`{"version":1,"provenance":"synthetic","frames":[`+strings.Join(rows, ",")+`]}`), provider, recording.Limits{})
	require.NoError(t, err)
	return d
}

func exercise(t *testing.T, provider string, actions []string, input, handshakes string) (string, bool, error) {
	t.Helper()
	d := synthetic(t, provider, actions...)
	var output bytes.Buffer
	consumed := false
	err := play(context.Background(), d, recording.Limits{}, handshakes, strings.NewReader(input), &output, func() error { consumed = true; return nil })
	return output.String(), consumed, err
}

func TestCodexDuplexAndRawResponses(t *testing.T) {
	response := `{ "id" : 1, "result": {"id":1,"html":"<tag>","x":0.123456789012345678901234567890,"escaped":"\u0061"} }`
	actions := []string{
		`>{"id":1,"method":"one","params":{"array":[1,"x",null]}}`,
		`>{"id":2,"method":"two"}`,
		`<{"method":"early","params":{"opaqueId":9007199254740993}}`,
		`<{"id":2,"error":{"code":-32000,"message":"synthetic"}}`,
		`<{"id":1,"method":"question","params":[{"opaque":true}]}`,
		`>{"id":1,"result":{"answer":"blue"}}`, "<" + response,
	}
	input := "{\"params\":{\"array\":[1,\"x\",null]},\"method\":\"one\",\"id\":\"live\"}\r\n" +
		"{\"id\":9007199254740993,\"method\":\"two\"}\n{\"id\":1,\"result\":{\"answer\":\"blue\"}}\n"
	out, consumed, err := exercise(t, "codex", actions, input, "")
	require.NoError(t, err)
	assert.True(t, consumed)
	assert.Equal(t, strings.Join([]string{actions[2][1:], `{"id":9007199254740993,"error":{"code":-32000,"message":"synthetic"}}`, actions[4][1:], strings.Replace(response, `"id" : 1`, `"id" : "live"`, 1), ""}, "\n"), out)
}

func TestCodexStrictFailures(t *testing.T) {
	base := []string{`>{"id":1,"method":"one","params":{"x":1}}`, `<{"id":1,"result":{}}`}
	req := `<{"id":"q","method":"approval","params":{"threadId":"child"}}`
	answer := `>{"id":"q","result":{}}`
	resolved := `<{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":"q"}}`
	for _, tc := range []struct {
		name       string
		frames     []string
		input      string
		diagnostic string
	}{
		{"EOF", base, "", "early client EOF"},
		{"method", base, "{\"id\":9,\"method\":\"other\"}\n", "client mismatch"},
		{"parameter", base, "{\"id\":9,\"method\":\"one\",\"params\":{\"x\":2}}\n", "client mismatch"},
		{"missing ID", base, "{\"method\":\"one\",\"params\":{\"x\":1}}\n", "invalid protocol ID"},
		{"null ID", base, "{\"id\":null,\"method\":\"one\"}\n", "invalid protocol ID"},
		{"boolean ID", base, "{\"id\":true,\"method\":\"one\"}\n", "invalid protocol ID"},
		{"extra field", base, "{\"id\":9,\"method\":\"one\",\"params\":{\"x\":1},\"extra\":null}\n", "client mismatch"},
		{"extra byte", base, "{\"id\":9,\"method\":\"one\",\"params\":{\"x\":1}}\n ", "extra client byte"},
		{"duplicate live IDs", []string{`>{"id":1,"method":"one"}`, `>{"id":2,"method":"two"}`}, "{\"id\":9,\"method\":\"one\"}\n{\"id\":9,\"method\":\"two\"}\n", "duplicate client ID"},
		{"duplicate recorded IDs", []string{`>{"id":1,"method":"one"}`, `<{"id":1,"result":{}}`, `>{"id":1,"method":"two"}`}, "{\"id\":9,\"method\":\"one\"}\n{\"id\":10,\"method\":\"two\"}\n", "duplicate client ID"},
		{"settled live ID reuse", []string{`>{"id":1,"method":"one"}`, `<{"id":1,"result":{}}`, `>{"id":2,"method":"two"}`}, "{\"id\":9,\"method\":\"one\"}\n{\"id\":9,\"method\":\"two\"}\n", "duplicate client ID"},
		{"unmatched response", []string{`<{"id":1,"result":{}}`}, "", "response without pending"},
		{"duplicate response", []string{`>{"id":1,"method":"one"}`, `<{"id":1,"result":{}}`, `<{"id":1,"result":{}}`}, "{\"id\":9,\"method\":\"one\"}\n", "response without pending"},
		{"pending client", []string{`>{"id":1,"method":"one"}`}, "{\"id\":9,\"method\":\"one\"}\n", "unfinished protocol requests"},
		{"pending reverse", []string{req}, "", "unfinished protocol requests"},
		{"wrong reverse ID", []string{req, answer}, "{\"id\":\"wrong\",\"result\":{}}\n", "client mismatch"},
		{"late answer", []string{req, resolved, answer}, answer[1:] + "\n", "unknown or duplicate reverse answer"},
		{"wrong resolution thread", []string{req, strings.Replace(resolved, "child", "root", 1)}, "", "unfinished protocol requests"},
		{"missing resolution thread", []string{req, `<{"method":"serverRequest/resolved","params":{"requestId":"q"}}`}, "", "unfinished protocol requests"},
		{"nonscalar resolution params", []string{req, `<{"method":"serverRequest/resolved","params":["q"]}`}, "", "unfinished protocol requests"},
		{"wrong resolution ID type", []string{req, `<{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":true}}`}, "", "unfinished protocol requests"},
		{"reverse ID reuse", []string{req, answer, req}, answer[1:] + "\n", "duplicate reverse request"},
		{"unknown answer", []string{answer}, answer[1:] + "\n", "unknown or duplicate reverse answer"},
		{"duplicate answer", []string{req, answer, answer}, strings.Repeat(answer[1:]+"\n", 2), "unknown or duplicate reverse answer"},
		{"nonobject", base, "[1,2]\n", "must be an object"},
		{"trailing JSON", base, "{} {}\n", "invalid JSON"},
		{"duplicate JSON key", base, "{\"id\":1,\"id\":2}\n", "duplicate JSON member"},
		{"oversize client", base, strings.Repeat("x", (2<<20)+1), "client frame byte limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, consumed, err := exercise(t, "codex", tc.frames, tc.input, "")
			require.ErrorContains(t, err, tc.diagnostic)
			assert.Contains(t, err.Error(), "ordinal ")
			assert.Equal(t, tc.name == "extra byte", consumed)
		})
	}
	_, consumed, err := exercise(t, "codex", []string{req, resolved}, "", "")
	require.NoError(t, err)
	assert.True(t, consumed)
}

func TestClaudeDuplexCancellationAndPayloadPreservation(t *testing.T) {
	actions := []string{
		`>{"type":"control_request","request_id":"old","request":{"request_id":"literal"}}`,
		`>{"type":"control_request","request_id":2,"request":{}}`,
		`<{"type":"notice","unknown":{"number":0.123456789012345678901234567890}}`,
		`<{"type":"control_response","response":{"request_id":2,"subtype":"error","error":"synthetic"}}`,
		`<{ "type" : "control_response", "response" : { "request_id" : "old", "payload":{"request_id":"old","text":"escaped \"request_id\": \"old\""}} }`,
		`<{"type":"control_request","request_id":"old","request":{}}`,
		`>{"type":"control_response","response":{"request_id":"old","response":{"behavior":"allow"}}}`,
		`<{"type":"control_request","request_id":"permission","request":{}}`,
		`<{"type":"control_cancel_request","request_id":"permission"}`,
	}
	input := "{\"type\":\"control_request\",\"request_id\":\"dynamic\",\"request\":{\"request_id\":\"literal\"}}\n" +
		"{\"type\":\"control_request\",\"request_id\":9007199254740993,\"request\":{}}\n" + actions[6][1:] + "\n"
	out, consumed, err := exercise(t, "claude", actions, input, "")
	require.NoError(t, err)
	assert.True(t, consumed)
	assert.Equal(t, strings.Join([]string{actions[2][1:], strings.Replace(actions[3][1:], `"request_id":2`, `"request_id":9007199254740993`, 1), strings.Replace(actions[4][1:], `"request_id" : "old"`, `"request_id" : "dynamic"`, 1), actions[5][1:], actions[7][1:], actions[8][1:], ""}, "\n"), out)
}

func TestClaudeStrictFailures(t *testing.T) {
	request := `>{"type":"control_request","request_id":"old","request":{"request_id":"literal"}}`
	response := `<{"type":"control_response","response":{"request_id":"old","payload":{"request_id":"literal"}}}`
	reverse := `<{"type":"control_request","request_id":"permission","request":{}}`
	answer := `>{"type":"control_response","response":{"request_id":"permission","response":{}}}`
	cancel := `<{"type":"control_cancel_request","request_id":"permission"}`
	input := strings.Replace(request[1:], `"request_id":"old"`, `"request_id":"dynamic"`, 1) + "\n"
	for _, tc := range []struct {
		name   string
		frames []string
		input  string
	}{
		{"pending client", []string{request}, input},
		{"pending reverse", []string{reverse}, ""},
		{"unmatched response", []string{response}, ""},
		{"unknown cancellation", []string{cancel}, ""},
		{"wrong ID type", []string{request, response}, strings.Replace(input, `"dynamic"`, `1`, 1)},
		{"empty ID", []string{request, response}, strings.Replace(input, `"dynamic"`, `""`, 1)},
		{"null ID", []string{request, response}, strings.Replace(input, `"dynamic"`, `null`, 1)},
		{"payload mismatch", []string{request, response}, strings.Replace(input, "literal", "changed", 1)},
		{"control kind mismatch", []string{request, response}, strings.Replace(input, "control_request", "notice", 1)},
		{"wrong reverse ID", []string{reverse, answer}, strings.Replace(answer[1:], "permission", "wrong", 1) + "\n"},
		{"duplicate recorded ID", []string{request, response, request}, input + strings.Replace(input, "dynamic", "second", 1)},
		{"duplicate live ID", []string{request, response, strings.Replace(request, "old", "second", 1)}, input + input},
		{"duplicate response", []string{request, response, response}, input},
		{"duplicate reverse", []string{reverse, answer, reverse}, answer[1:] + "\n"},
		{"duplicate answer", []string{reverse, answer, answer}, strings.Repeat(answer[1:]+"\n", 2)},
		{"unknown answer", []string{answer}, answer[1:] + "\n"},
		{"answer after cancellation", []string{reverse, cancel, answer}, answer[1:] + "\n"},
		{"missing response ID", []string{`<{"type":"control_response","response":{}}`}, ""},
		{"missing reverse ID", []string{`<{"type":"control_request","request":{}}`}, ""},
		{"nonobject response", []string{`<{"type":"control_response","response":[]}`}, ""},
		{"missing response object", []string{`<{"type":"control_response"}`}, ""},
		{"malformed client response", []string{reverse, answer}, "{\"type\":\"control_response\",\"response\":null}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, consumed, err := exercise(t, "claude", tc.frames, tc.input, "")
			require.Error(t, err)
			assert.False(t, consumed)
		})
	}
}

func TestFullObjectMatchingAndNumberLexemes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, payload := range []string{`{"x":true}`, `{"x":9007199254740993}`, `{"x":1.0}`, `{"x":[1,2]}`, `{"x":"1"}`} {
			t.Run(provider+payload, func(t *testing.T) {
				_, _, err := exercise(t, provider, []string{">" + payload}, "{\"x\":1}\n", "")
				require.ErrorContains(t, err, "client mismatch")
			})
		}
		wire := `{ "type":"notice", "number":0.123456789012345678901234567890, "opaqueId":9007199254740993, "request_id":"literal" }`
		out, _, err := exercise(t, provider, []string{"<" + wire}, "", "")
		require.NoError(t, err)
		assert.Equal(t, wire+"\n", out)
		_, _, err = exercise(t, provider, []string{`>{"a":"a","b":[1,2]}`}, "{\"b\":[1,2],\"a\":\"\\u0061\"}\n", "")
		require.NoError(t, err)
		_, _, err = exercise(t, provider, []string{`>{"a":"a","b":[1,2]}`}, "{\"a\":\"a\",\"b\":[2,1]}\n", "")
		require.Error(t, err)
	}
}

func TestCodexTestControls(t *testing.T) {
	actions := []string{`<{"id":"question","method":"future/question","params":{"threadId":"thread"}}`, `>{"id":"question","result":{"answer":"literal"}}`, `<{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":"question"}}`}
	answer := actions[1][1:] + "\n"
	settle := "{\"id\":8,\"method\":\"replay/answer\"}\n"
	assertion := "{\"id\":9,\"method\":\"replay/assert\"}\n"
	for _, mode := range []string{"", "answer", "answer-and-assert"} {
		input := answer
		if mode != "" {
			input += settle
		}
		if mode == "answer-and-assert" {
			input += assertion
		}
		out, done, err := exercise(t, "codex", actions, input, mode)
		require.NoError(t, err)
		assert.True(t, done)
		if mode != "" {
			assert.Less(t, strings.Index(out, `"id":8`), strings.Index(out, "serverRequest/resolved"))
		}
	}
	for _, bad := range []string{"", assertion, `{"id":8,"method":"other"}`, `{"id":null,"method":"replay/answer"}`, `{"id":8,"method":"replay/answer","params":{}}`, `{"id":8,"method":"replay/answer","extra":true}`, `{"method":"replay/answer"}`} {
		t.Run(bad, func(t *testing.T) {
			out, done, err := exercise(t, "codex", actions, answer+bad+"\n", "answer-and-assert")
			require.Error(t, err)
			assert.False(t, done)
			assert.NotContains(t, out, "serverRequest/resolved")
		})
	}
	_, done, err := exercise(t, "codex", actions, answer+settle, "answer-and-assert")
	require.Error(t, err)
	assert.False(t, done)
	_, _, err = exercise(t, "codex", actions, answer+settle, "")
	require.ErrorContains(t, err, "extra client byte")
}

type shortOutput struct{}

func (shortOutput) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestTransportLimitsAndCompletionErrors(t *testing.T) {
	d := synthetic(t, "codex", `<{}`)
	err := play(context.Background(), d, recording.Limits{}, "", strings.NewReader(""), shortOutput{}, nil)
	require.ErrorIs(t, err, io.ErrShortWrite)
	_, err = newAdapter("another-agent")
	require.ErrorContains(t, err, "unsupported native protocol")
	err = play(context.Background(), d, recording.Limits{}, "", strings.NewReader(""), io.Discard, func() error { return io.ErrClosedPipe })
	require.ErrorIs(t, err, io.ErrClosedPipe)
	for _, tc := range []struct {
		name, wire, input string
		limits            recording.Limits
		wantErr           bool
	}{
		{"payload boundary accepted", ">{}", "{}\r\n", recording.Limits{MaxFrameBytes: 3, MaxBytes: 4}, false},
		{"client payload exclusive", ">{}", "{}\n", recording.Limits{MaxFrameBytes: 2}, true},
		{"server payload exclusive", "<{}", "", recording.Limits{MaxFrameBytes: 2}, true},
		{"client traffic inclusive", ">{}", "{}\n", recording.Limits{MaxBytes: 3}, false},
		{"client traffic exceeded", ">{}", "{}\r\n", recording.Limits{MaxBytes: 3}, true},
		{"server traffic exceeded", "<{}", "", recording.Limits{MaxBytes: 2}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := synthetic(t, "codex", tc.wire)
			err := play(context.Background(), doc, tc.limits, "", strings.NewReader(tc.input), io.Discard, nil)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	for _, direction := range []string{">{}", "<{}"} {
		doc := synthetic(t, "codex", direction, direction)
		err = play(context.Background(), doc, recording.Limits{MaxBytes: 5}, "", strings.NewReader("{}\n{}\n"), io.Discard, nil)
		require.ErrorContains(t, err, "traffic byte limit")
	}
}
