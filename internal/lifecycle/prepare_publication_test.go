package lifecycle

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPrepareClaudeStartupHistoryUsesPublishedRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	target := filepath.Join(t.TempDir(), "fixture")
	var staging string
	fixture, err := prepare(PrepareOptions{FixtureDir: target, Agent: "claude", Case: "SDK-CASE-033"}, func(stage, _ string) error {
		staging = stage
		return nil
	})
	require.NoError(t, err)
	current, present, err := CurrentArtifact(fixture.dir, fixture)
	require.NoError(t, err)
	require.True(t, present)
	before, err := history.HashTree(current.Path)
	require.NoError(t, err)

	for _, phase := range []string{"published", "refreshed"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "refreshed" {
				require.NoError(t, RefreshRoutes(fixture))
			}
			route, err := ResolveRoute(fixture, "mock")
			require.NoError(t, err)
			key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(route.Workspace, "-")
			project := filepath.Join(route.Env["CLAUDE_CONFIG_DIR"], "projects", key)
			require.DirExists(t, project, "the published route must discover history before claiming an execution")
			files, err := filepath.Glob(filepath.Join(project, "*.jsonl"))
			require.NoError(t, err)
			require.NotEmpty(t, files)
			var cwdRows int
			for _, name := range files {
				data, err := os.ReadFile(name)
				require.NoError(t, err)
				require.NotContains(t, string(data), staging)
				scanner := bufio.NewScanner(bytes.NewReader(data))
				for scanner.Scan() {
					var row tool.Object
					require.NoError(t, json.Unmarshal(scanner.Bytes(), &row))
					if cwd := tool.Str(row["cwd"]); cwd != "" {
						require.Equal(t, route.Workspace, cwd)
						cwdRows++
					}
				}
				require.NoError(t, scanner.Err())
			}
			require.Positive(t, cwdRows)
			var snapshot struct {
				HistoryFiles map[string]string `json:"historyFiles"`
			}
			require.NoError(t, tool.ReadJSON(filepath.Join(ResourceRoot(fixture.dir), "routes", "mock.json"), &snapshot))
			require.NoError(t, history.Check(TemplateRoot(fixture.dir, "mock"), snapshot.HistoryFiles))
		})
	}
	after, err := history.HashTree(current.Path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestPrepareImportsBuiltinCoreWithoutChangingRecordingIdentity(t *testing.T) {
	selected, _, err := builtin.Select("codex", "SDK-CASE-001")
	require.NoError(t, err)
	source, err := builtin.Extract(selected)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(source)) })
	original, err := corpus.ReadMaterialDescriptor(source)
	require.NoError(t, err)
	require.NotNil(t, original)
	require.Equal(t, 3, original.FormatVersion)

	target := filepath.Join(t.TempDir(), "fixture")
	fixture, err := Prepare(PrepareOptions{FixtureDir: target, Agent: "codex", Case: "SDK-CASE-001"})
	require.NoError(t, err)
	current, present, err := CurrentArtifact(target, fixture)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, original.ID, current.ID)
	require.Equal(t, original.FormatVersion, current.FormatVersion)
	require.NoError(t, builtin.CheckMaterial(MaterialRoot(target, current.ID), selected))
	for _, name := range []string{"executions", "record", "verify"} {
		require.NoDirExists(t, filepath.Join(PackageRoot(target, current.ID), name))
	}
}

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
					data, err := os.ReadFile(filepath.Join(stage, "runtime", "routes", action+".json"))
					require.NoError(t, err)
					require.NotContains(t, string(data), stage)
					for _, name := range routeEnvironmentEntries {
						link, err := os.Readlink(filepath.Join(RouteRoot(stage, action), name))
						require.NoError(t, err)
						require.Equal(t, filepath.Join(TemplateRoot(final, action), name), link)
					}
				}
				launcher, err := os.ReadFile(LauncherPath(stage, "main"))
				require.NoError(t, err)
				require.NotContains(t, string(launcher), stage)
				require.Contains(t, string(launcher), final)
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
			require.NotContains(t, fixture.Workspace, stagePath)
			_, err = OpenFixture(target)
			require.NoError(t, err)
			_, err = Prepare(PrepareOptions{FixtureDir: target, Agent: "codex", Case: "SDK-CASE-001"})
			require.ErrorContains(t, err, "already exists")
		})
	}
}

func TestPrepareModelCatalogUsesPublishedBaseline(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CODEX_HOME", config)
	original := []byte("model_catalog_json = 'original-catalog.json'\n")
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.toml"), original, 0o600))
	catalog := []byte("{\"models\":[]}\n")
	require.NoError(t, os.WriteFile(filepath.Join(config, "original-catalog.json"), catalog, 0o600))
	fixture, err := Prepare(PrepareOptions{FixtureDir: filepath.Join(t.TempDir(), "fixture"), Agent: "codex", Case: "SDK-CASE-001"})
	require.NoError(t, err)
	for _, root := range []string{BaselineRoot(fixture.dir), TemplateRoot(fixture.dir, "record")} {
		data, err := os.ReadFile(filepath.Join(root, "config", "config.toml"))
		require.NoError(t, err)
		var settings tool.Object
		require.NoError(t, toml.Unmarshal(data, &settings))
		reference := tool.Str(settings["model_catalog_json"])
		require.Equal(t, filepath.Join(fixture.Config, "model-catalog.json"), reference)
		actual, err := os.ReadFile(reference)
		require.NoError(t, err)
		require.Equal(t, catalog, actual)
	}
	actual, err := os.ReadFile(filepath.Join(config, "config.toml"))
	require.NoError(t, err)
	require.Equal(t, original, actual)
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
