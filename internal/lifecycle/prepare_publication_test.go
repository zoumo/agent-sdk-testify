package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPreparePublishesOnlyCompleteFixture(t *testing.T) {
	for _, fault := range []string{"before-publish", "concurrent-target", "success"} {
		t.Run(fault, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "fixture")
			var stagePath string
			fixture, err := prepare(PrepareOptions{FixtureDir: target, Agent: "codex", Case: "SDK-CASE-001"}, func(stage, final string) error {
				stagePath = stage
				require.Equal(t, filepath.Dir(stage), filepath.Dir(final))
				_, err := OpenFixture(final)
				require.Error(t, err, "the target must remain invisible until publication")
				for _, name := range []string{"fixture.json", "current.json", "executions/index.json", "packages/index.json", "runtime/routes/mock.json", "locks/action.lock"} {
					require.FileExists(t, filepath.Join(stage, name))
				}
				for _, action := range []string{"record", "mock"} {
					data, err := os.ReadFile(filepath.Join(stage, "runtime/routes", action+".json"))
					require.NoError(t, err)
					require.NotContains(t, string(data), stage)
					for _, name := range routeEnvironmentEntries {
						link, err := os.Readlink(filepath.Join(RouteRoot(stage, action), name))
						require.NoError(t, err)
						require.Equal(t, filepath.Join(TemplateRoot(final, action), name), link)
					}
				}
				if fault == "before-publish" {
					return errors.New("injected publication failure")
				}
				if fault == "concurrent-target" {
					require.NoError(t, os.Mkdir(final, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(final, "other-owner"), []byte("preserve"), 0o600))
				}
				return nil
			})
			require.NoDirExists(t, stagePath)
			if fault != "success" {
				require.Error(t, err)
				_, err := OpenFixture(target)
				require.Error(t, err)
				if fault == "concurrent-target" {
					require.FileExists(t, filepath.Join(target, "other-owner"))
				}
				return
			}
			require.NoError(t, err)
			final, err := filepath.EvalSymlinks(target)
			require.NoError(t, err)
			require.Equal(t, final, fixture.dir)
			require.False(t, strings.Contains(fixture.Workspace, stagePath))
			_, err = OpenFixture(target)
			require.NoError(t, err)
			_, err = Prepare(PrepareOptions{FixtureDir: target, Agent: "codex", Case: "SDK-CASE-001"})
			require.ErrorContains(t, err, "already exists")
		})
	}
}

func TestRecordWorkerReturnsCallbackFileFailure(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	capture := policy.capture
	policy.capture = func(ctx context.Context, run RecordContext) error {
		if err := os.WriteFile(filepath.Join(run.ArtifactRoot(), "raw"), []byte("blocked"), 0o600); err != nil {
			return err
		}
		return capture(ctx, run)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := RunRecord(ctx, recordTransactionRequest(fixtureDir, 0), policy); done <- err }()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "not a directory")
	case <-ctx.Done():
		t.Fatal("worker callback did not return its file failure")
	}
	require.Zero(t, packages)
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "failed", index.Executions[0].Status)
}

func TestMockPreparationSealsWithExecution(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	material, err := LatestArtifact(fixtureDir, fixture)
	require.NoError(t, err)
	result, err := RunMock(t.Context(), MockRequest{FixtureDir: fixtureDir, Connection: "main", Artifact: *material, ProgressFD: -1}, mockPolicyFunc(func(_ context.Context, run MockContext) error {
		for _, name := range []string{"prepared-plan.json", "isolation.json"} {
			if _, err := os.Stat(filepath.Join(run.ExecutionDir(), name)); err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, err)
	dir := filepath.Dir(result.ExecutionPath)
	var aggregate tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(dir, "result.json"), &aggregate))
	for _, name := range []string{"prepared-plan.json", "isolation.json"} {
		path := filepath.Join(dir, name)
		hash, err := tool.FileDigest(path)
		require.NoError(t, err)
		require.Equal(t, hash, tool.Obj(aggregate["files"])["executions/"+result.ExecutionID+"/"+name])
		original, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
		require.Error(t, validateExecutionAggregate(fixtureDir, result.MaterialID, result.ExecutionID, tool.Str(aggregate["executionSHA256"])))
		require.NoError(t, os.WriteFile(path, original, 0o600))
	}
}
