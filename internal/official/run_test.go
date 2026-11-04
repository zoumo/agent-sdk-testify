package official

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	testdata "github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/replay"
	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "preparation-kernel":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			var o preparationOptions
			err := json.Unmarshal([]byte(os.Args[2]), &o)
			if err == nil {
				if o.Mode == "record" {
					var plan tool.Object
					err = tool.ReadJSON(filepath.Join(o.Output, "plan.json"), &plan)
					if err == nil {
						err = capture.Run(ctx, capture.Options{Output: filepath.Join(o.Output, "raw"), Command: append([]string{tool.Str(plan["cli"])}, tool.Strings(plan["args"])...), CWD: tool.Str(plan["project"]), StdinEOFAction: "close", CleanupTimeout: time.Second}, capture.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
					}
				} else {
					var source *os.File
					source, err = os.Open(filepath.Join(o.Materials, "raw", "raw.jsonl"))
					if err == nil {
						var document *recording.Document
						document, err = recording.Read(source, o.Provider, recording.Limits{})
						err = errors.Join(err, source.Close())
						if err == nil {
							err = replay.Run(ctx, document, recording.Limits{}, "", os.Stdin, os.Stdout, -1)
						}
					}
				}
			}
			stop()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "native-preparation-fixture":
			var o preparationOptions
			if err := tool.ReadJSON(os.Args[2], &o); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			if err := runPreparationFixture(context.Background(), o); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "--version":
			fmt.Println("native-test any-version")
			os.Exit(0)
		case "synthetic-agent":
			r := bufio.NewReader(os.Stdin)
			if _, e := r.ReadBytes('\n'); e != nil {
				os.Exit(1)
			}
			fmt.Println(`{"id":1,"result":{}}`)
			for {
				if _, e := r.ReadByte(); e != nil {
					break
				}
			}
			os.Exit(0)
		case "synthetic-remote-cleanup":
			interrupted := make(chan os.Signal, 1)
			signal.Notify(interrupted, os.Interrupt)
			fmt.Println("ready")
			<-interrupted
			os.Exit(0)
		}
	}
	cleanup, err := testutil.PrepareBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func sample(t *testing.T, id string) preparationOptions {
	t.Helper()
	source := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(source, "raw"), 0o700))
	frames := []tool.Object{
		{"ordinal": 1, "direction": "c2s", "channel": "protocol", "epoch": 1, "elapsedMs": 0, "wire": tool.Object{"id": 1, "method": "ping"}},
		{"ordinal": 2, "direction": "s2c", "channel": "protocol", "epoch": 1, "elapsedMs": 1, "wire": tool.Object{"id": 1, "result": tool.Object{}}},
	}
	var data []byte
	for _, f := range frames {
		b, e := json.Marshal(f)
		require.NoError(t, e)
		data = append(append(data, b...), '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(source, "raw", "raw.jsonl"), data, 0o600))
	planPath := filepath.Join(source, "plan.json")
	require.NoError(t, tool.WriteJSON(planPath, tool.Object{"provider": "codex", "caseId": id, "project": filepath.Join(source, "original-cwd"), "driverProfile": "continuation", "args": []string{"synthetic-agent"}, "scenario": tool.Object{"files": tool.Object{"numbers.txt": "7\n11\n"}}, "agentMock": tool.Object{"sha256": "deliberately-different", "version": "old-version"}}))
	planDigest, err := tool.FileDigest(planPath)
	require.NoError(t, err)
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), tool.Object{
		"provenance": "external",
		"planSHA256": planDigest,
		"savedFiles": tool.Object{},
	}))
	return preparationOptions{Mode: "verify", Materials: source, Output: filepath.Join(t.TempDir(), "run")}
}

func TestSharedMaterialsContainNoExecutableSteps(t *testing.T) {
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	o := preparationOptions{Materials: filepath.Join(repository, "data", "scenarios"), Case: "SDK-CASE-008", Provider: "claude"}
	p, e := loadMaterials(o)
	require.NoError(t, e)
	require.NotContains(t, tool.Obj(p["scenario"]), "steps")
	require.Equal(t, "approval", p["driverProfile"])
	o.Materials = t.TempDir()
	require.NoError(t, tool.WriteJSON(filepath.Join(o.Materials, "case.json"), tool.Object{"scenario": tool.Object{"files": tool.Object{"../outside": "bad"}}}))
	p, e = loadMaterials(o)
	require.NoError(t, e)
	require.ErrorContains(t, tool.WriteFiles(t.TempDir(), tool.Obj(tool.Obj(p["scenario"])["files"])), "escapes")
}

// This fixture is limited to native preparation/history tests. It calls the
// actual preparation and capture/replay kernels; public orchestration is tested
// through installed CLI conformance, never this fixture.
func runPreparationFixture(ctx context.Context, o preparationOptions) error {
	plan, err := loadMaterials(o)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(o.Output, "project"), 0o700); err != nil {
		return err
	}
	plan["project"] = filepath.Join(o.Output, "project")
	for _, name := range []string{"home", "config", "tmp", "materials"} {
		if err := os.MkdirAll(filepath.Join(o.Output, name), 0o700); err != nil {
			return err
		}
	}
	if err := tool.WriteFiles(tool.Str(plan["project"]), tool.Obj(tool.Obj(plan["scenario"])["files"])); err != nil {
		return err
	}
	if o.Mode == "record" {
		if o.HistorySeed != "" {
			if err := PrepareHistorySeed(o.HistorySeed, o.Output, plan); err != nil {
				return err
			}
		}
		if plan["driverProfile"] == "existing-children" && o.HistorySeed == "" {
			return errors.New("existing-child recording needs supplied saved-history materials")
		}
		if err := PrepareNative(ctx, &o.Options, plan, o.Output); err != nil {
			return err
		}
	} else {
		if _, err := history.Materialize(o.Materials, o.Output); err != nil {
			return err
		}
		if tool.Str(plan["provider"]) == "claude" {
			if err := history.MaterializeClaude(o.Materials, o.Output, tool.Str(plan["project"]), plan); err != nil {
				return err
			}
		}
	}
	plan["root"], plan["home"], plan["config"] = o.Output, filepath.Join(o.Output, "home"), filepath.Join(o.Output, "config")
	if err := tool.WriteJSON(filepath.Join(o.Output, "plan.json"), plan); err != nil {
		return err
	}
	if err := tool.WriteJSON(filepath.Join(o.Output, "materials", "case.json"), plan); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	o.Provider = tool.Str(plan["provider"])
	encoded, err := json.Marshal(o)
	if err != nil {
		return err
	}
	request := Request{Case: tool.Str(plan["caseId"]), Materials: filepath.Join(o.Output, "materials"), Workspace: tool.Str(plan["project"]), Output: o.Output, Peer: DriverPeer{Command: tool.Command{Command: self, Args: []string{"preparation-kernel", string(encoded)}}}}
	path := filepath.Join(o.Output, "request.json")
	if err := tool.WriteJSON(path, request); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, o.Driver, append(o.DriverArgs, "--request", path)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = request.Workspace, os.Stdout, os.Stderr
	cmd.Env, err = driverEnvironment(o.Options, plan)
	if err != nil {
		return err
	}
	err = cmd.Run()
	if o.Mode == "record" {
		err = errors.Join(err, corpus.WriteSourceManifest(o.Output, tool.Result{Status: "passed"}), scrubNative(o.Output))
	}
	return err
}

// The test fixture describes preparation inputs, not a second product command.
type preparationOptions struct {
	Options
	Mode, Materials, Case, Provider, Output, Driver, HistorySeed string
	DriverArgs                                                   []string
}

func loadMaterials(o preparationOptions) (tool.Object, error) {
	return catalog.Resolve(testdata.Scenarios(), catalog.ResolveInput{Mode: o.Mode, Materials: o.Materials, Case: o.Case, Provider: o.Provider, Model: o.Model})
}
