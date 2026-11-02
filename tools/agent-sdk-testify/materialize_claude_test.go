package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeMovedMaterialsUseCurrentSDKAndDerivedHistory(t *testing.T) {
	o := sample(t, "SDK-CASE-033")
	o.Mode, o.DriverArgs = "simulate", nil
	o.Driver = filepath.Join(t.TempDir(), "sdk-driver")
	build := exec.Command("go", "build", "-o", o.Driver, "../../tests/simulation/drivers")
	log, err := build.CombinedOutput()
	require.NoError(t, err, "%s", log)
	const session = "11111111-1111-4111-8111-111111111111"
	originalCWD := filepath.Join(t.TempDir(), "no-longer-present")
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(originalCWD, "-")
	name := filepath.Join("config/projects", key, session+".jsonl")
	var saved bytes.Buffer
	expected := []object{}
	for i, kind := range []string{"user", "assistant"} {
		id := []string{"u1", "a1"}[i]
		var parent any
		if i == 1 {
			parent = "u1"
		}
		message := object{"content": "saved " + kind}
		require.NoError(t, json.NewEncoder(&saved).Encode(object{"type": kind, "uuid": id, "parentUuid": parent, "sessionId": session, "cwd": originalCWD, "message": message}))
		expected = append(expected, object{"type": kind, "uuid": id, "session_id": session, "message": message})
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(o.Materials, name)), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(o.Materials, name), saved.Bytes(), 0600))
	plan := object{"provider": "claude", "caseId": "SDK-CASE-033", "project": originalCWD, "driverProfile": "history", "scenario": object{"files": object{"numbers.txt": "7\n11\n"}}}
	require.NoError(t, writeJSON(filepath.Join(o.Materials, "plan.json"), plan))
	require.NoError(t, writeJSON(filepath.Join(o.Materials, "report.json"), object{"receipts": []object{{"get_session_messages": expected}}}))
	rows := []object{
		{"type": "control_request", "request_id": "init", "request": object{"subtype": "initialize", "hooks": nil}},
		{"type": "control_response", "response": object{"subtype": "success", "request_id": "init", "response": object{}}},
		{"type": "user", "session_id": "default", "parent_tool_use_id": nil, "message": object{"role": "user", "content": "task"}},
		{"type": "system", "subtype": "init", "session_id": session, "cwd": originalCWD},
		{"type": "result", "session_id": session, "subtype": "success", "is_error": false, "result": "done"},
	}
	var raw bytes.Buffer
	for i, row := range rows {
		direction := "s2c"
		if i == 0 || i == 2 {
			direction = "c2s"
		}
		require.NoError(t, json.NewEncoder(&raw).Encode(object{"ordinal": i + 1, "direction": direction, "channel": "protocol", "epoch": 1, "elapsedMs": 0, "wire": row}))
	}
	require.NoError(t, os.WriteFile(filepath.Join(o.Materials, "raw/raw.jsonl"), raw.Bytes(), 0600))
	// Move the full material package and remove its original location.
	moved := filepath.Join(t.TempDir(), "adopted-copy")
	require.NoError(t, copyTree(o.Materials, moved))
	require.NoError(t, os.RemoveAll(o.Materials))
	o.Materials = moved
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	summary, err := run(ctx, o)
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(o.Output, "stderr.log"))
		t.Log(string(b))
	}
	require.NoError(t, err)
	require.NotEmpty(t, summary["materializationSHA256"])
	var transformed struct {
		ExecutionProject string
		Transformations  []struct{ Source, SourceSHA256, Derived, DerivedSHA256 string }
	}
	require.NoError(t, readJSON(filepath.Join(o.Output, "materialization.json"), &transformed))
	canonicalOutput, err := filepath.EvalSymlinks(o.Output)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(canonicalOutput, "project"), transformed.ExecutionProject)
	require.Len(t, transformed.Transformations, 1)
	change := transformed.Transformations[0]
	original, err := os.ReadFile(filepath.Join(o.Materials, name))
	require.NoError(t, err)
	require.Equal(t, saved.Bytes(), original)
	originalCopy, err := os.ReadFile(filepath.Join(o.Output, name))
	require.NoError(t, err)
	require.Equal(t, original, originalCopy)
	derived, err := os.ReadFile(filepath.Join(o.Output, change.Derived))
	require.NoError(t, err)
	require.Equal(t, bytes.ReplaceAll(original, []byte(originalCWD), []byte(transformed.ExecutionProject)), derived)
	for _, item := range []struct{ path, hash string }{{filepath.Join(o.Materials, change.Source), change.SourceSHA256}, {filepath.Join(o.Output, change.Derived), change.DerivedSHA256}} {
		digest, err := fileDigest(item.path)
		require.NoError(t, err)
		require.Equal(t, item.hash, digest)
	}
	unchanged, err := os.ReadFile(filepath.Join(o.Materials, "raw/raw.jsonl"))
	require.NoError(t, err)
	require.Equal(t, raw.Bytes(), unchanged)
	require.NoDirExists(t, originalCWD)
}

func TestClaudeHistoryDerivationRejectsCorruptionAndPreservesOtherBytes(t *testing.T) {
	for _, tc := range []struct {
		name, filename, source string
		fail                   bool
	}{
		{"lexemes", "record.jsonl", `{"cwd":"/original/project","future":1.2500,"id":"keep","nested":{"cwd":"do-not-edit"}}` + "\n", false},
		{"pretty-sidecar", "agent.meta.json", "{\n  \"cwd\": \"/original/project\",\n  \"future\": 9007199254740993\n}\n", false},
		{"duplicate", "record.jsonl", `{"cwd":"/original/project","cwd":"/other"}` + "\n", true},
		{"foreign", "record.jsonl", `{"cwd":"/other/project"}` + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, root, workspace := t.TempDir(), t.TempDir(), t.TempDir()
			path := filepath.Join(source, "config/projects/-original-project", tc.filename)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte(tc.source), 0600))
			err := materializeClaude(source, root, workspace, object{"project": "/original/project"})
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(workspace, "-")
				derived, err := os.ReadFile(filepath.Join(root, "config/projects", key, tc.filename))
				require.NoError(t, err)
				require.Equal(t, bytes.ReplaceAll([]byte(tc.source), []byte("/original/project"), []byte(workspace)), derived)
			}
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.source, string(original))
		})
	}
}
