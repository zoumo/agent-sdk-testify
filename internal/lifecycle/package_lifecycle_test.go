package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func logPackageState(t *testing.T, fixtureDir, materialID string) {
	t.Helper()
	var current Current
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "current.json"), &current))
	files, err := history.HashTree(PackageRoot(fixtureDir, materialID))
	require.NoError(t, err)
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	results := map[string]tool.Object{}
	matches, err := filepath.Glob(filepath.Join(PackageRoot(fixtureDir, materialID), "executions", "*", "receipts", "*.result.json"))
	require.NoError(t, err)
	for _, path := range matches {
		var result tool.Object
		require.NoError(t, tool.ReadJSON(path, &result))
		reference, err := filepath.Rel(PackageRoot(fixtureDir, materialID), path)
		require.NoError(t, err)
		results[filepath.ToSlash(reference)] = result
	}
	value, err := json.Marshal(tool.Object{"current": current, "executions": index.Executions, "files": files, "results": results})
	require.NoError(t, err)
	t.Logf("publication-evidence=%s", value)
}

func TestPackagePublicationCleanupFailuresKeepCommittedCurrent(t *testing.T) {
	for _, failure := range []string{"route-snapshot", "route-restore", "authentication-cleanup"} {
		t.Run(failure, func(t *testing.T) {
			fixtureDir := prepareRecordTransactionFixture(t)
			var packages int
			policy := successfulRecordPolicy(t, &packages)
			packageMaterial := policy.finalize
			policy.finalize = func(ctx context.Context, run RecordContext) (*corpus.Material, error) {
				artifact, err := packageMaterial(ctx, run)
				if err != nil {
					return nil, err
				}
				logPackageState(t, fixtureDir, artifact.ID)
				switch failure {
				case "route-snapshot":
					path := filepath.Join(run.Fixture().ResourceRoot, "routes", "record.json")
					require.NoError(t, os.Remove(path))
					require.NoError(t, os.Mkdir(path, 0o700))
				case "route-restore":
					path := filepath.Join(TemplateRoot(fixtureDir, "record"), "workspace")
					require.NoError(t, os.RemoveAll(path))
					require.NoError(t, os.WriteFile(path, []byte("blocked"), 0o600))
				case "authentication-cleanup":
					config := run.Execution().Config
					require.NoError(t, os.Rename(config, config+"-preserved"))
					require.NoError(t, os.WriteFile(config, []byte("blocked"), 0o600))
				}
				return artifact, nil
			}
			result, err := RunRecord(t.Context(), recordTransactionRequest(fixtureDir, 0), policy)
			require.Error(t, err)
			require.NotNil(t, result.Material)
			require.True(t, result.PublicationCommitted)
			require.Equal(t, result.Material.ID, result.MaterialID)
			require.Equal(t, result.Material.ID, currentIDForTest(t, fixtureDir))
			require.FileExists(t, result.ReceiptPath)
			var durable tool.Object
			require.NoError(t, tool.ReadJSON(result.ResultPath, &durable))
			require.Equal(t, true, durable["publicationCommitted"])
			require.Equal(t, "passed", durable["connectionStatus"])
			require.Equal(t, result.Material.ID, durable["materialId"])
			require.Equal(t, "failed", durable["status"])
			require.FileExists(t, filepath.Join(filepath.Dir(result.ExecutionPath), "result.json"))
			require.NoDirExists(t, filepath.Join(filepath.Dir(result.ExecutionPath), "capture"))
			logPackageState(t, fixtureDir, result.MaterialID)
			assertRecordLockAvailable(t, fixtureDir)
		})
	}
}

func currentIDForTest(t *testing.T, fixtureDir string) string {
	t.Helper()
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	current, err := readCurrent(fixtureDir, fixture.FixtureID)
	require.NoError(t, err)
	return current.MaterialID
}

type prefixResultWriter struct {
	bytes.Buffer
	limit int
}

func (w *prefixResultWriter) SetWriteDeadline(time.Time) error { return nil }

func (w *prefixResultWriter) Write(data []byte) (int, error) {
	if w.limit >= 0 && len(data) > w.limit {
		n, _ := w.Buffer.Write(data[:w.limit])
		return n, errors.New("synthetic notification delivery failure")
	}
	return w.Buffer.Write(data)
}

func TestPackagePublicationPartialAndLostNotification(t *testing.T) {
	for _, limit := range []int{-1, 0, 37} {
		t.Run(map[int]string{-1: "complete", 0: "lost", 37: "partial"}[limit], func(t *testing.T) {
			fixtureDir := prepareRecordTransactionFixture(t)
			run, err := beginRecordTransaction(recordTransactionRequest(fixtureDir, 0))
			require.NoError(t, err)
			defer run.releaseLock()
			var packages int
			policy := successfulRecordPolicy(t, &packages)
			require.NoError(t, policy.capture(t.Context(), run.context()))
			artifact, err := policy.finalize(t.Context(), run.context())
			require.NoError(t, err)
			_, _, err = run.settleConnection("passed")
			require.NoError(t, err)
			before, err := tool.FileDigest(filepath.Join(run.dir, "execution.json"))
			require.NoError(t, err)
			logPackageState(t, fixtureDir, artifact.ID)
			require.NoError(t, run.publish(*artifact))
			path, value, err := actionResult(&run.transactionContext, "passed", nil)
			require.NoError(t, err)
			value["executionSHA256"] = before
			require.NoError(t, writeAtomic(path, value))
			writer := &prefixResultWriter{limit: limit}
			err = deliverActionResult(t.Context(), &run.transactionContext, writer, path, value)
			if limit < 0 {
				require.NoError(t, err)
				var notification tool.Object
				require.NoError(t, json.Unmarshal(writer.Bytes(), &notification))
				var durable tool.Object
				require.NoError(t, tool.ReadJSON(path, &durable))
				require.Equal(t, notification, durable)
			} else {
				require.ErrorContains(t, err, "notification delivery failure")
				require.Equal(t, limit, writer.Len())
				require.NotContains(t, writer.String(), "\n")
				var durable tool.Object
				require.NoError(t, tool.ReadJSON(path, &durable))
				require.Equal(t, true, durable["publicationCommitted"])
				require.Contains(t, durable["deliveryError"], "notification delivery failure")
				require.Equal(t, "passed", durable["connectionStatus"])
			}
			after, err := tool.FileDigest(filepath.Join(run.dir, "execution.json"))
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, artifact.ID, currentIDForTest(t, fixtureDir))
			logPackageState(t, fixtureDir, artifact.ID)
		})
	}
}

func TestPackagePublicationBeforeMoveFailureKeepsOldCurrent(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	packageMaterial := policy.finalize
	policy.finalize = func(ctx context.Context, run RecordContext) (*corpus.Material, error) {
		artifact, err := packageMaterial(ctx, run)
		if err != nil {
			return nil, err
		}
		stage := filepath.Join(PackageRoot(fixtureDir, artifact.ID), ".material-staging")
		require.NoError(t, os.Mkdir(stage, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(stage, "blocked"), []byte("blocked"), 0o600))
		logPackageState(t, fixtureDir, artifact.ID)
		return artifact, nil
	}
	result, err := RunRecord(t.Context(), recordTransactionRequest(fixtureDir, 0), policy)
	require.Error(t, err)
	require.False(t, result.PublicationCommitted)
	stateBefore.requireUnchanged(t, fixtureDir)
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.NotNil(t, index.Executions[0].Publishing)
	require.FileExists(t, filepath.Join(filepath.Dir(result.ExecutionPath), "capture", "raw", "raw.jsonl"))
	logPackageState(t, fixtureDir, result.MaterialID)
}

func TestPackageRecordConnectionsCanCaptureConcurrently(t *testing.T) {
	fixtureDir := prepareMultiRecordTransactionFixture(t)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	capture := policy.capture
	started := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWorkers := func() { releaseOnce.Do(func() { close(release) }) }
	policy.capture = func(ctx context.Context, run RecordContext) error {
		started <- run.Connection()
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return capture(ctx, run)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		releaseWorkers()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("record workers did not stop")
		}
	})
	results := make(chan *RecordResult, 2)
	failures := make(chan error, 2)
	for _, connection := range []string{"main", "probe"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			request := recordTransactionRequest(fixtureDir, 0)
			request.Connection = connection
			result, err := RunRecord(ctx, request, policy)
			results <- result
			failures <- err
		}()
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case connection := <-started:
			seen[connection] = true
		case <-ctx.Done():
			t.Fatal("connections did not reach capture concurrently")
		}
	}
	require.Len(t, seen, 2)
	releaseWorkers()
	completed := make([]*RecordResult, 0, 2)
	for range 2 {
		select {
		case result := <-results:
			completed = append(completed, result)
		case <-ctx.Done():
			t.Fatal("record workers did not return: ", ctx.Err())
		}
		select {
		case err := <-failures:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("record workers did not report: ", ctx.Err())
		}
	}
	first, second := completed[0], completed[1]
	require.Equal(t, first.ExecutionID, second.ExecutionID)
	require.Equal(t, first.MaterialID, second.MaterialID)
	require.Equal(t, 1, packages)
	require.FileExists(t, filepath.Join(filepath.Dir(first.ExecutionPath), "result.json"))
	for _, result := range []*RecordResult{first, second} {
		require.FileExists(t, result.ReceiptPath)
		var durable tool.Object
		require.NoError(t, tool.ReadJSON(result.ResultPath, &durable))
		receipt, err := ResolvePackageReference(fixtureDir, result.MaterialID, tool.Str(durable["receipt"]))
		require.NoError(t, err)
		canonical, err := filepath.EvalSymlinks(receipt)
		require.NoError(t, err)
		require.Equal(t, result.ReceiptPath, canonical)
		require.FileExists(t, receipt)
	}
}

func TestPackageAggregateRejectsIdentityAndInventoryTampering(t *testing.T) {
	for _, field := range []string{"materialId", "executionId", "execution", "executionSHA256", "files", "extra-file"} {
		t.Run(field, func(t *testing.T) {
			fixtureDir := prepareRecordTransactionFixture(t)
			var packages int
			result, err := RunRecord(t.Context(), recordTransactionRequest(fixtureDir, 0), successfulRecordPolicy(t, &packages))
			require.NoError(t, err)
			hash, err := tool.FileDigest(result.ExecutionPath)
			require.NoError(t, err)
			require.NoError(t, validateExecutionAggregate(fixtureDir, result.MaterialID, result.ExecutionID, hash))
			path := filepath.Join(filepath.Dir(result.ExecutionPath), "result.json")
			var aggregate tool.Object
			require.NoError(t, tool.ReadJSON(path, &aggregate))
			switch field {
			case "extra-file":
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "unexpected"), []byte("extra"), 0o600))
			case "files":
				aggregate[field] = tool.Object{}
				require.NoError(t, writeAtomic(path, aggregate))
			default:
				aggregate[field] = "wrong-identity"
				require.NoError(t, writeAtomic(path, aggregate))
			}
			require.Error(t, validateExecutionAggregate(fixtureDir, result.MaterialID, result.ExecutionID, hash))
		})
	}
}

func TestPackageControlRejectsPriorAndUnknownSchemas(t *testing.T) {
	for _, version := range []int{6, 8} {
		for _, name := range []string{"fixture.json", "current.json", "executions/index.json", "packages/index.json", "execution.json", "official.json"} {
			t.Run(strconv.Itoa(version)+"/"+name, func(t *testing.T) {
				fixtureDir := prepareRecordTransactionFixture(t)
				var packages int
				result, err := RunRecord(t.Context(), recordTransactionRequest(fixtureDir, 0), successfulRecordPolicy(t, &packages))
				require.NoError(t, err)
				fixture, err := OpenFixture(fixtureDir)
				require.NoError(t, err)
				path := filepath.Join(fixtureDir, name)
				if name == "execution.json" {
					path = result.ExecutionPath
				}
				value := tool.Object{}
				if name != "official.json" {
					require.NoError(t, tool.ReadJSON(path, &value))
				}
				value["schemaVersion"] = version
				require.NoError(t, writeAtomic(path, value))
				switch name {
				case "execution.json":
					_, err = ReadExecution(fixtureDir, fixture, path)
				case "official.json":
					err = checkOfficialIdle(fixtureDir)
				default:
					_, err = OpenFixture(fixtureDir)
				}
				require.ErrorContains(t, err, "schemaVersion "+strconv.Itoa(version))
			})
		}
	}
}

func TestPackagePublicationIndexFailureRecoversFromSealedExecution(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	run, err := beginRecordTransaction(recordTransactionRequest(fixtureDir, 0))
	require.NoError(t, err)
	defer run.releaseLock()
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	require.NoError(t, policy.capture(t.Context(), run.context()))
	require.NoError(t, run.preserveConnectionReceipt())
	artifact, err := policy.finalize(t.Context(), run.context())
	require.NoError(t, err)
	_, _, err = run.settleConnection("passed")
	require.NoError(t, err)
	logPackageState(t, fixtureDir, artifact.ID)
	require.NoError(t, run.commitPublication(*artifact))
	control := filepath.Dir(IndexPath(fixtureDir))
	require.NoError(t, os.Chmod(control, 0o500))
	t.Cleanup(func() { _ = os.Chmod(control, 0o700) })
	finishErr := run.finishPublication()
	require.Error(t, finishErr)
	require.NoError(t, writeTransactionResult(t.Context(), &run.transactionContext, nil, finishErr))
	require.NoError(t, os.Chmod(control, 0o700))
	require.Equal(t, artifact.ID, currentIDForTest(t, fixtureDir))
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "open", index.Executions[0].Status)
	var durable tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(run.dir, "receipts", "main.result.json"), &durable))
	require.Equal(t, true, durable["publicationCommitted"])
	require.Equal(t, artifact.ID, durable["materialId"])
	require.Equal(t, "passed", durable["executionStatus"])
	before, err := history.HashTree(PackageRoot(fixtureDir, artifact.ID))
	require.NoError(t, err)
	indexBefore, err := os.ReadFile(IndexPath(fixtureDir))
	require.NoError(t, err)
	_, err = OpenFixture(fixtureDir)
	require.NoError(t, err)
	indexAfter, err := os.ReadFile(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, indexBefore, indexAfter, "Open must not recover or write")
	require.NoError(t, run.releaseLock())
	lock, exclusive, err := beginActionLock(fixtureDir)
	require.NoError(t, err)
	require.True(t, exclusive)
	require.NoError(t, lock.Close())
	index, err = readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "passed", index.Executions[0].Status)
	require.Equal(t, artifact.ID, index.Executions[0].ArtifactID)
	after, err := history.HashTree(PackageRoot(fixtureDir, artifact.ID))
	require.NoError(t, err)
	require.Equal(t, before, after, "recovery must not rewrite sealed domains")
	logPackageState(t, fixtureDir, artifact.ID)
}

func TestPackageCallerCoordinationCoversHostAfterAgentReturn(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	var usage sync.Mutex
	// B obtained an earlier route under caller coordination, before A claimed.
	usage.Lock()
	var stale tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(ResourceRoot(fixtureDir), "routes", "record.json"), &stale))
	usage.Unlock()
	var releaseHostOnce sync.Once
	releaseHost := func() { releaseHostOnce.Do(usage.Unlock) }
	usage.Lock() // A owns the complete host lifetime, starting before route reads.
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	_, err = ResolveRoute(fixture, "record")
	require.NoError(t, err)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	capture := policy.capture
	claimed := make(chan string, 1)
	releaseCapture := make(chan struct{})
	var releaseOnce sync.Once
	releaseCaptureWorker := func() { releaseOnce.Do(func() { close(releaseCapture) }) }
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		releaseCaptureWorker()
		releaseHost()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("host workers did not stop")
		}
	})
	policy.capture = func(ctx context.Context, run RecordContext) error {
		claimed <- run.OverlayRoot()
		select {
		case <-releaseCapture:
		case <-ctx.Done():
			return ctx.Err()
		}
		return capture(ctx, run)
	}
	type outcome struct {
		result *RecordResult
		err    error
	}
	returned := make(chan outcome, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		result, err := RunRecord(ctx, recordTransactionRequest(fixtureDir, 0), policy)
		returned <- outcome{result, err}
	}()
	var overlay string
	select {
	case overlay = <-claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not claim")
	}
	before, err := history.HashTree(filepath.Join(overlay, "workspace"))
	require.NoError(t, err)
	nextHostStarted := make(chan string, 1)
	waiting := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		close(waiting)
		usage.Lock()
		defer usage.Unlock()
		// The previously obtained route can be read, but is never used to launch B.
		// Once admitted, B must obtain the current route again.
		_ = stale["workspace"]
		var snapshot tool.Object
		if err := tool.ReadJSON(filepath.Join(ResourceRoot(fixtureDir), "routes", "mock.json"), &snapshot); err != nil {
			nextHostStarted <- err.Error()
			return
		}
		nextHostStarted <- tool.Str(snapshot["materialId"])
	}()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("B did not start")
	}
	select {
	case <-nextHostStarted:
		t.Fatal("B used its stale route while A was active")
	default:
	}
	after, err := history.HashTree(filepath.Join(overlay, "workspace"))
	require.NoError(t, err)
	require.Equal(t, before, after, "B must not touch the active overlay before its own claim")
	releaseCaptureWorker()
	var completed outcome
	select {
	case completed = <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not return")
	}
	require.NoError(t, completed.err)
	select {
	case <-nextHostStarted:
		t.Fatal("B entered after protocol return but before A host shutdown")
	default:
	}
	// A's host/peer shutdown and settlement finish after the agent call returns.
	releaseHost()
	select {
	case id := <-nextHostStarted:
		require.Equal(t, completed.result.MaterialID, id)
	case <-time.After(5 * time.Second):
		t.Fatal("B did not enter after caller release")
	}
}
