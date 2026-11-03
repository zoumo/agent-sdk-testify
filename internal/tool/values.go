// Package tool contains shared process, file and JSON primitives used by the
// three public commands.
package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// Object is the lossless-enough JSON object shape used between stages.
// Native protocol bytes stay in protocol/recording and are never rebuilt here.
type Object = map[string]any

func Obj(v any) Object {
	m, _ := v.(map[string]any)
	if m == nil {
		return Object{}
	}
	return m
}

func Str(v any) string {
	s, _ := v.(string)
	return s
}

func Strings(v any) []string {
	var out []string
	for _, x := range Array(v) {
		out = append(out, Str(x))
	}
	return out
}

func Array(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if a, ok := v.([]string); ok {
		out := make([]any, len(a))
		for i, s := range a {
			out[i] = s
		}
		return out
	}
	return nil
}

func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func FileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("source is not a regular file: %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return DigestBytes(b), nil
}
