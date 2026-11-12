package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestFixtureRootRelativeReferenceValidation(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "runtime", "routes", "mock", "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("fixture\n"), 0o600))

	reference, err := FixtureReference(root, workspace)
	require.NoError(t, err)
	require.Equal(t, "runtime/routes/mock/workspace", reference)
	resolved, err := ResolveFixtureReference(root, reference)
	require.NoError(t, err)
	require.Equal(t, workspace, resolved)

	for _, invalid := range []string{"", "/absolute", "../outside", "runtime/../routes", "./runtime", "runtime/", "runtime//routes", `runtime\routes`} {
		t.Run(invalid, func(t *testing.T) {
			_, err := ResolveFixtureReference(root, invalid)
			require.Error(t, err)
		})
	}
	_, err = FixtureReference(root, "relative")
	require.ErrorContains(t, err, "must be absolute")
	_, err = FixtureReference(root, filepath.Join(filepath.Dir(root), filepath.Base(root)+"-outside"))
	require.Error(t, err)
}

func TestFixtureReferenceRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "runtime")
	require.NoError(t, os.Symlink(outside, link))
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "workspace"), 0o700))

	_, err := ResolveFixtureReference(root, "runtime/workspace")
	require.ErrorContains(t, err, "symlink")
	_, err = FixtureReference(root, filepath.Join(link, "workspace"))
	require.ErrorContains(t, err, "symlink")
}

func TestFixedLayoutDerivation(t *testing.T) {
	root := t.TempDir()
	require.Equal(t, filepath.Join(root, "runtime"), ResourceRoot(root))
	require.Equal(t, filepath.Join(root, "runtime", "baseline"), BaselineRoot(root))
	require.Equal(t, filepath.Join(root, "runtime", "baseline", "workspace"), BaselineWorkspace(root))
	require.Equal(t, filepath.Join(root, "runtime", "routes", "mock"), RouteRoot(root, "mock"))
	require.Equal(t, filepath.Join(root, "runtime", "routes", "mock", "workspace"), RouteWorkspace(root, "mock"))
	require.Equal(t, filepath.Join(root, "runtime", "templates", "mock"), TemplateRoot(root, "mock"))
	require.Equal(t, filepath.Join(root, "runtime", "launchers", "peer-main"), LauncherPath(root, "main"))
	require.Equal(t, filepath.Join(root, "packages", "material", "material"), MaterialRoot(root, "material"))
	require.Equal(t, filepath.Join(root, "runtime", "executions", "execution"), ExecutionOverlayRoot(root, "execution"))
	require.Equal(t, filepath.Join(root, "packages", "material", "executions", "execution"), PackageExecutionRoot(root, "material", "execution"))
	require.Equal(t, filepath.Join(root, "source", "plan.json"), SourcePlanPath(root))
	require.Equal(t, "packages/material/material", MaterialReference("material"))
}

func TestPackageReferencesRejectWrongBaseAndSymlinks(t *testing.T) {
	root := t.TempDir()
	packageRoot := PackageRoot(root, "material-id")
	require.NoError(t, os.MkdirAll(filepath.Join(packageRoot, "material"), 0o700))
	for _, invalid := range []string{"", "/absolute", "../other/material", "./material", "material/../executions", `material\raw`} {
		_, err := ResolvePackageReference(root, "material-id", invalid)
		require.Error(t, err, invalid)
	}
	for _, id := range []string{"", "../other", "/absolute", "a/b"} {
		_, err := ResolvePackageReference(root, id, "material")
		require.Error(t, err, id)
	}
	_, err := PackageReference(root, "material-id", MaterialRoot(root, "other-id"))
	require.Error(t, err)
	require.NoError(t, os.Symlink(filepath.Join(packageRoot, "material"), filepath.Join(packageRoot, "alias")))
	_, err = ResolvePackageReference(root, "material-id", "alias/raw")
	require.ErrorContains(t, err, "symlink")
	require.NoError(t, os.Symlink(packageRoot, PackageRoot(root, "alias-id")))
	_, err = ResolvePackageReference(root, "alias-id", "material")
	require.ErrorContains(t, err, "symlink")
}

func TestPreparedFixtureResolvesFixedLayoutAndOmitsMachineLocalPaths(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	canonical, err := filepath.EvalSymlinks(fixtureDir)
	require.NoError(t, err)
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	require.Equal(t, BaselineWorkspace(canonical), fixture.Workspace)
	require.Equal(t, RouteWorkspace(canonical, "mock"), fixture.Routes["mock"].Workspace)
	require.Equal(t, RouteRoot(canonical, "mock"), filepath.Dir(fixture.Routes["mock"].Workspace))

	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor))
	require.EqualValues(t, 7, descriptor["schemaVersion"])
	for _, key := range []string{"resourceRoot", "workspace", "home", "config", "tmp"} {
		require.NotContains(t, descriptor, key)
	}
	toolIdentity := tool.Obj(descriptor["tool"])
	require.NotContains(t, toolIdentity, "command")
	require.NotContains(t, toolIdentity, "args")
}
