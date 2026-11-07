package build

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func TestRepositoryEntryLayout(t *testing.T) {
	root, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		pkg     string
		wantPkg string
	}{
		{name: "library", pkg: ".", wantPkg: "agenttestify"},
		{name: "cli", pkg: "./cmd/agent-sdk-testify", wantPkg: "main"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "list", "-f", "{{.Name}}", test.pkg)
			cmd.Dir = filepath.Clean(root)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Equal(t, test.wantPkg, strings.TrimSpace(string(out)))
		})
	}
}
