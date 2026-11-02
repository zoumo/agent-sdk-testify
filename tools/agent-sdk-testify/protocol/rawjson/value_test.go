package rawjson_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/rawjson"
	"testing"
)

func TestRawAndSurgicalReplacement(t *testing.T) {
	original := []byte(`  { "z":9007199254740993,"response":{ "request_id":"old", "payload":{"request_id":"literal","decimal":0.123456789012345678901234567890,"exp":1e+09}}, "a":[-0,true,null,"\u0061"] } `)
	v, err := rawjson.Parse(original)
	require.NoError(t, err)
	assert.Equal(t, original, v.Raw())
	replacement, err := rawjson.Parse([]byte(`"new"`))
	require.NoError(t, err)
	changed, err := v.Replace([]string{"response", "request_id"}, replacement)
	require.NoError(t, err)
	assert.Equal(t, `  { "z":9007199254740993,"response":{ "request_id":"new", "payload":{"request_id":"literal","decimal":0.123456789012345678901234567890,"exp":1e+09}}, "a":[-0,true,null,"\u0061"] } `, string(changed.Raw()))
	assert.Equal(t, original, v.Raw())
	original[2] = 'X'
	copied := v.Raw()
	copied[2] = 'X'
	assert.Equal(t, byte('{'), v.Raw()[2])
	_, err = v.Replace([]string{"missing"}, replacement)
	require.Error(t, err)
	_, err = v.Replace(nil, replacement)
	require.Error(t, err)
	_, err = v.Replace([]string{"z", "nested"}, replacement)
	require.Error(t, err)
}

func TestExactSemanticComparison(t *testing.T) {
	for _, tt := range []struct {
		a, b  string
		equal bool
	}{
		{`{"a":1,"b":[true,"a"]}`, `{"b":[true,"\u0061"],"a":1}`, true},
		{`1`, ` 1 `, true}, {`1`, `1.0`, false}, {`1e3`, `1000`, false}, {`-0`, `0`, false},
		{`"\ud83d\ude00"`, `"😀"`, true},
		{`9007199254740993`, `9007199254740992`, false}, {`true`, `1`, false}, {`"1"`, `1`, false},
		{`{"unknown":null}`, `{}`, false}, {`[1,2]`, `[2,1]`, false}, {`{"x":1}`, `{"x":2}`, false},
	} {
		t.Run(tt.a+"/"+tt.b, func(t *testing.T) {
			a, err := rawjson.Parse([]byte(tt.a))
			require.NoError(t, err)
			b, err := rawjson.Parse([]byte(tt.b))
			require.NoError(t, err)
			assert.Equal(t, tt.equal, rawjson.Equal(a, b))
		})
	}
}

func TestRejectsInvalidAndDuplicateJSON(t *testing.T) {
	for _, input := range []string{``, `{} {}`, `{"a":1,"a":2}`, `{"x":[{"a":1,"\u0061":2}]}`, `{"x":NaN}`, `{"x":01}`, `{"x":Infinity}`, `{"x":}`, `[1,]`, `"\ud800"`, `"\udfff"`, `"\ud800\u0041"`, string([]byte{'"', 0xff, '"'})} {
		t.Run(input, func(t *testing.T) { _, err := rawjson.Parse([]byte(input)); require.Error(t, err) })
	}
}

func TestAccessors(t *testing.T) {
	v, err := rawjson.Parse([]byte(`{"n":9223372036854775807,"a":[true,null],"s":"escaped\\quote\"","f":1.0}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"n", "a", "s", "f"}, v.Keys())
	keys := v.Keys()
	keys[0] = "changed"
	assert.Equal(t, "n", v.Keys()[0])
	n, _ := v.Member("n")
	i, err := n.Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(9223372036854775807), i)
	a, _ := v.Member("a")
	items := a.Elements()
	require.Len(t, items, 2)
	b, err := items[0].Bool()
	require.NoError(t, err)
	assert.True(t, b)
	assert.Equal(t, rawjson.Null, items[1].Kind())
	f, _ := v.Member("f")
	_, err = f.Int64()
	require.Error(t, err)
	_, err = v.String()
	require.Error(t, err)
	_, err = v.Bool()
	require.Error(t, err)
	assert.False(t, rawjson.Equal(rawjson.Value{}, rawjson.Value{}))
}

func FuzzParseAndReplace(f *testing.F) {
	for _, s := range []string{`{}`, `{"id":1,"a":["x",{"b":null}]}`, `{"id":1,"id":2}`, `"escaped\\\""`, `[1,true,null]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := rawjson.Parse(data)
		if err != nil {
			return
		}
		require.Equal(t, data, v.Raw())
		again, err := rawjson.Parse(v.Raw())
		require.NoError(t, err)
		require.True(t, rawjson.Equal(v, again))
		if v.Kind() == rawjson.Object {
			for _, key := range v.Keys() {
				child, _ := v.Member(key)
				same, err := v.Replace([]string{key}, child)
				require.NoError(t, err)
				require.Equal(t, v.Raw(), same.Raw())
			}
		}
	})
}
