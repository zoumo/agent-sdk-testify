package agenttestify_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

func TestLibraryExportReprepareAfterDeletingOriginalSources(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	plan := writeLibraryPlan(t, root)
	sourcePlan := readFile(t, plan)
	seed := filepath.Join(root, "external-seed")
	donor, err := agenttestify.Prepare(agenttestify.PrepareOptions{FixtureDir: filepath.Join(root, "seed-donor"), Agent: "codex", Case: "SDK-CASE-001"})
	require.NoError(t, err)
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{FixtureDir: donor.FixtureDir, Output: seed}))
	seedBefore := treeContents(t, seed)
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: filepath.Join(root, "original"), Plan: plan, HistorySeed: seed,
	})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(donor.FixtureDir))
	require.Equal(t, seedBefore, treeContents(t, filepath.Join(prepared.FixtureDir, "source", "history-seed", "source")))
	recorded, _, _, err := runLibraryRecord(t, prepared, agenttestify.RecordAgentOptions{
		Command: writeLibraryRecordPeer(t, root, "success"), NativeArgs: []string{"app-server", "--listen", "stdio://"}, Timeout: 10 * time.Second,
	}, "{\"id\":1,\"method\":\"synthetic\"}\n")
	require.NoError(t, err)
	material := assertPublishedLibraryRecord(t, prepared, recorded)
	packageBytes := treeContents(t, filepath.Dir(material.Path))
	packageInput := filepath.Join(root, "package-input")
	for name, data := range packageBytes {
		target := filepath.Join(packageInput, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
		require.NoError(t, os.WriteFile(target, data, 0o600))
	}
	exported := filepath.Join(root, "export")
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{FixtureDir: prepared.FixtureDir, Output: exported}))
	before := treeContents(t, exported)
	hashes := treeHashes(t, exported)
	t.Logf("before core inventory: %v", hashes)
	descriptor := readLibraryJSON(t, filepath.Join(exported, "material.json"))
	require.EqualValues(t, 3, descriptor["formatVersion"])
	require.Equal(t, hashes["source-plan.json"], descriptor["sourcePlanSHA256"])
	require.Equal(t, hashes["plan.json"], descriptor["planSHA256"])
	require.NotEqual(t, descriptor["sourcePlanSHA256"], descriptor["planSHA256"])
	require.Equal(t, sourcePlan, before["source-plan.json"])
	require.Equal(t, seedBefore, treeContents(t, filepath.Join(exported, "history", "seed", "source")))
	require.NotContains(t, descriptor, "path")
	require.NotContains(t, descriptor, "files")
	require.NoError(t, os.RemoveAll(prepared.FixtureDir))
	require.NoError(t, os.RemoveAll(seed))
	require.NoError(t, os.Remove(plan))

	imported, err := agenttestify.Prepare(agenttestify.PrepareOptions{FixtureDir: filepath.Join(root, "new-location"), Recording: exported})
	require.NoError(t, err)
	expected := *material
	expected.Path = imported.Current.Path
	require.Equal(t, expected, *imported.Current)
	require.Equal(t, before, treeContents(t, imported.Current.Path))
	require.NoError(t, os.RemoveAll(exported))
	reopened, err := agenttestify.Open(imported.FixtureDir)
	require.NoError(t, err)
	input := newControlledInput("{\"id\":1,\"method\":\"synthetic\"}\n")
	run, err := reopened.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{NativeArgs: []string{"app-server", "--listen", "stdio://"}}, agenttestify.StartMockAgentStreams{
		Reader: input, Writer: &testWriteCloser{}, Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	cleanupMockRun(t, run)
	overlay := filepath.Join(imported.FixtureDir, "runtime", "executions", run.Start.ExecutionID)
	for name, path := range map[string]string{"workspace": run.Start.Workspace, "home": run.Start.Home, "config": run.Start.Config, "tmp": run.Start.Tmp} {
		require.Equal(t, filepath.Join(overlay, name), path)
		require.DirExists(t, path)
	}
	require.Equal(t, imported.ProtocolCwd, run.Start.ProtocolCwd)
	require.Equal(t, run.Start.Home, run.Start.Env["HOME"])
	require.Equal(t, run.Start.Config, run.Start.Env["CODEX_HOME"])
	awaitFullConsumption(t, run)
	require.NoError(t, input.CloseEOF())
	result, err := run.Wait(testContext(t))
	require.NoError(t, err)
	report, err := reopened.ExecutionReport(result.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, result.Execution, report)
	require.True(t, report.Complete)
	packageRoot := filepath.Dir(imported.Current.Path)
	notice := readLibraryJSON(t, result.ResultPath)
	require.EqualValues(t, 3, notice["schemaVersion"])
	require.Equal(t, material.ID, notice["materialId"])
	for key, path := range map[string]string{"execution": result.ExecutionPath, "receipt": result.ReceiptPath} {
		require.Equal(t, path, filepath.Join(packageRoot, filepath.FromSlash(notice[key].(string))))
	}
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{FixtureDir: imported.FixtureDir, Output: exported}))
	require.Equal(t, before, treeContents(t, exported))
	require.Equal(t, hashes, treeHashes(t, exported))
	t.Logf("after core inventory: %v", treeHashes(t, exported))

	full, err := agenttestify.Prepare(agenttestify.PrepareOptions{FixtureDir: filepath.Join(root, "full-package"), Recording: packageInput})
	require.NoError(t, err)
	require.Equal(t, material.ID, full.Current.ID)
	require.Equal(t, packageBytes, treeContents(t, filepath.Dir(full.Current.Path)))
	require.Empty(t, readLibraryJSON(t, filepath.Join(full.FixtureDir, "executions", "index.json"))["executions"])
	require.NoError(t, os.RemoveAll(packageInput))
	archived, err := full.ExecutionReport(recorded.ExecutionID)
	require.ErrorContains(t, err, "absent")
	require.Nil(t, archived)
	result, _, _, err = runLibraryMock(t, full, agenttestify.MockAgentOptions{NativeArgs: []string{"app-server", "--listen", "stdio://"}}, "{\"id\":1,\"method\":\"synthetic\"}\n")
	require.NoError(t, err)
	require.NotEqual(t, recorded.ExecutionID, result.ExecutionID)
	for name, original := range packageBytes {
		require.Equal(t, original, readFile(t, filepath.Join(filepath.Dir(full.Current.Path), filepath.FromSlash(name))), name)
	}
}

func TestLibraryCallerCoordinatesBeforeClaimThroughHostShutdown(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	var usage sync.Mutex
	// An old route remains readable. The caller owns SDK/discovery admission,
	// beginning before route reads and ending after all its host/peer work.
	stale := prepared.Routes["mock"]
	usage.Lock()
	held := true
	stopped := make(chan struct{})
	defer func() {
		if held {
			usage.Unlock()
		}
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Error("B remained active during cleanup")
		}
	}()
	requested, entered, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		defer close(stopped)
		close(requested)
		usage.Lock()
		defer usage.Unlock()
		close(entered)
		// Refresh after admission; do not launch from the previously read route.
		fixture, err := agenttestify.Open(prepared.FixtureDir)
		if err != nil {
			finished <- err
			return
		}
		input := newControlledInput(libraryMockInput)
		run, err := fixture.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: input, Writer: &testWriteCloser{}, Stderr: &bytes.Buffer{},
		})
		if err != nil {
			finished <- err
			return
		}
		_ = input.CloseEOF()
		_, err = run.Wait(t.Context())
		finished <- err
	}()
	<-requested
	assertWaiting := func() {
		t.Helper()
		select {
		case <-entered:
			t.Fatal("B started discovery/SDK before A host shutdown")
		default:
		}
	}
	assertWaiting() // A has read its route but has not claimed yet.
	workspace, err := filepath.EvalSymlinks(stale.Workspace)
	require.NoError(t, err)
	require.DirExists(t, workspace)
	templateBefore := treeHashes(t, filepath.Join(prepared.FixtureDir, "runtime", "templates"))
	assertWaiting()
	require.Equal(t, templateBefore, treeHashes(t, filepath.Join(prepared.FixtureDir, "runtime", "templates")))
	sourceBefore := treeHashes(t, filepath.Join(prepared.FixtureDir, "source"))
	input := newControlledInput(libraryMockInput)
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
		Reader: input, Writer: &testWriteCloser{}, Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	cleanupMockRun(t, run)
	awaitFullConsumption(t, run)
	overlayBefore := treeHashes(t, filepath.Dir(run.Start.Config))
	assertWaiting() // The old route is still readable after A claims.
	_, err = filepath.EvalSymlinks(stale.Workspace)
	require.NoError(t, err)
	require.Equal(t, overlayBefore, treeHashes(t, filepath.Dir(run.Start.Config)))
	require.Equal(t, sourceBefore, treeHashes(t, filepath.Join(prepared.FixtureDir, "source")))
	require.NoError(t, input.CloseEOF())
	_, err = run.Wait(testContext(t))
	require.NoError(t, err)
	assertWaiting() // Agent return alone does not end A's host lifetime.
	usage.Unlock()  // A has now stopped its host and every peer.
	held = false
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("B did not finish after caller release")
	}
}
