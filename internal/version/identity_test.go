package version

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testRevision = "c4d2e2a5cdd67f61ef41b7932b66ca454c4077a8"
	testSum      = "h1:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
	testTime     = "2026-11-08T18:12:20Z"
	testVersion  = "v0.0.0-20261108181220-c4d2e2a5cdd6"
)

func TestEmbeddedIdentityUsesExactBuildInformation(t *testing.T) {
	t.Run("development main module", func(t *testing.T) {
		identity, err := embeddedIdentity(&debug.BuildInfo{
			GoVersion: "go1.26.10",
			Main: debug.Module{
				Path:    Module,
				Version: "v0.0.0-20261108181220-c4d2e2a5cdd6",
			},
			Settings: []debug.BuildSetting{
				{Key: "vcs", Value: "git"},
				{Key: "vcs.revision", Value: testRevision},
				{Key: "vcs.time", Value: testTime},
				{Key: "vcs.modified", Value: "false"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, BuildIdentity{
			Module:    Module,
			Version:   "v0.0.0-20261108181220-c4d2e2a5cdd6",
			Revision:  testRevision,
			Time:      testTime,
			GoVersion: "go1.26.10",
		}, identity)
	})

	t.Run("dependency module", func(t *testing.T) {
		identity, err := embeddedIdentity(&debug.BuildInfo{
			GoVersion: "go1.26.10",
			Main:      debug.Module{Path: "example.com/consumer", Version: "(devel)"},
			Deps: []*debug.Module{{
				Path:    Module,
				Version: "v0.2.0",
				Sum:     testSum,
			}},
		})
		require.NoError(t, err)
		require.Equal(t, BuildIdentity{
			Module:    Module,
			Version:   "v0.2.0",
			Sum:       testSum,
			GoVersion: "go1.26.10",
		}, identity)
	})

	t.Run("devel version with VCS revision", func(t *testing.T) {
		identity, err := embeddedIdentity(&debug.BuildInfo{
			GoVersion: "go1.26.10",
			Main:      debug.Module{Path: Module, Version: "(devel)"},
			Settings: []debug.BuildSetting{
				{Key: "vcs", Value: "git"},
				{Key: "vcs.revision", Value: testRevision},
				{Key: "vcs.time", Value: testTime},
				{Key: "vcs.modified", Value: "false"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "(devel)", identity.Version)
		require.Equal(t, testRevision, identity.Revision)
		require.Equal(t, testTime, identity.Time)
	})
}

func TestEmbeddedIdentityFallsBackWhenBuildInfoOmitsDependency(t *testing.T) {
	info := &debug.BuildInfo{
		GoVersion: "go1.26.3",
		Main:      debug.Module{Path: "example.com/sdk/internal/mocktest.test", Version: "(devel)"},
	}
	provided := agentTestifyIdentity("go1.26.3")

	identity, err := resolveEmbeddedIdentity(info, provided)
	require.NoError(t, err)
	require.Equal(t, BuildIdentity{
		Module:    Module,
		Version:   provided.Version,
		Sum:       provided.Sum,
		Revision:  provided.Revision,
		Time:      provided.Time,
		GoVersion: provided.GoVersion,
	}, identity)
}

func TestEmbeddedIdentityKeepsSelfProvenIdentity(t *testing.T) {
	info := &debug.BuildInfo{
		GoVersion: "go1.26.10",
		Main:      debug.Module{Path: "example.com/consumer", Version: "(devel)"},
		Deps: []*debug.Module{{
			Path:    Module,
			Version: "v0.2.0",
			Sum:     testSum,
		}},
	}
	provided := agentTestifyIdentity("go1.26.10")
	provided.Version = "v0.2.0"

	identity, err := resolveEmbeddedIdentity(info, provided)
	require.NoError(t, err)
	require.Equal(t, "v0.2.0", identity.Version)
	require.Equal(t, testSum, identity.Sum)
	require.Empty(t, identity.Revision)

	provided.Version = "v0.3.0"
	_, err = resolveEmbeddedIdentity(info, provided)
	require.ErrorContains(t, err, "version")
	require.ErrorContains(t, err, "conflicts with build information")
}

func TestExplicitEmbeddedIdentityFailsClosed(t *testing.T) {
	info := &debug.BuildInfo{
		GoVersion: "go1.26.3",
		Main:      debug.Module{Path: "example.com/sdk/internal/mocktest.test", Version: "(devel)"},
	}
	for _, test := range []struct {
		name   string
		mutate func(*EmbeddedIdentity)
		want   string
	}{
		{
			name: "missing Go version",
			mutate: func(identity *EmbeddedIdentity) {
				identity.GoVersion = ""
			},
			want: "requires the Go version",
		},
		{
			name: "invalid Go version",
			mutate: func(identity *EmbeddedIdentity) {
				identity.GoVersion = "1.26.3"
			},
			want: "invalid Go version",
		},
		{
			name: "different Go version",
			mutate: func(identity *EmbeddedIdentity) {
				identity.GoVersion = "go1.26.4"
			},
			want: "differs from build Go version",
		},
		{
			name: "non-exact version",
			mutate: func(identity *EmbeddedIdentity) {
				identity.Version = "0.1.0-dev"
			},
			want: "is not exact",
		},
		{
			name: "invalid sum",
			mutate: func(identity *EmbeddedIdentity) {
				identity.Sum = "h1:not-a-sum"
			},
			want: "exact h1 module sum",
		},
		{
			name: "short revision",
			mutate: func(identity *EmbeddedIdentity) {
				identity.Revision = testRevision[:39]
			},
			want: "full 40- or 64-character lowercase revision",
		},
		{
			name: "invalid time",
			mutate: func(identity *EmbeddedIdentity) {
				identity.Time = "yesterday"
			},
			want: "RFC 3339 commit time",
		},
		{
			name: "pseudo timestamp mismatch",
			mutate: func(identity *EmbeddedIdentity) {
				identity.Time = "2026-11-08T18:12:21Z"
			},
			want: "timestamp differs from commit time",
		},
		{
			name: "pseudo revision mismatch",
			mutate: func(identity *EmbeddedIdentity) {
				identity.Revision = "d4d2e2a5cdd67f61ef41b7932b66ca454c4077a8"
			},
			want: "revision differs from commit revision",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provided := agentTestifyIdentity("go1.26.3")
			test.mutate(provided)
			_, err := resolveEmbeddedIdentity(info, provided)
			require.ErrorContains(t, err, test.want)
		})
	}

	_, err := resolveEmbeddedIdentity(nil, agentTestifyIdentity("go1.26.3"))
	require.ErrorContains(t, err, "build info is absent")

	_, err = resolveEmbeddedIdentity(info, nil)
	require.ErrorContains(t, err, "does not contain module")
}

func agentTestifyIdentity(goVersion string) *EmbeddedIdentity {
	return &EmbeddedIdentity{
		Version:   testVersion,
		Sum:       testSum,
		Revision:  testRevision,
		Time:      testTime,
		GoVersion: goVersion,
	}
}

func TestEmbeddedIdentityFailsClosed(t *testing.T) {
	validSettings := []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: testRevision},
		{Key: "vcs.modified", Value: "false"},
	}
	for _, test := range []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{
			name: "missing module",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: "example.com/consumer", Version: "(devel)"},
			},
			want: "does not contain module",
		},
		{
			name: "missing Go version",
			info: &debug.BuildInfo{
				Main:     debug.Module{Path: Module, Version: "v0.1.0"},
				Settings: validSettings,
			},
			want: "omits the Go version",
		},
		{
			name: "invalid commit time",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: Module, Version: "v0.1.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs", Value: "git"},
					{Key: "vcs.revision", Value: testRevision},
					{Key: "vcs.time", Value: "yesterday"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			want: "invalid VCS commit time",
		},
		{
			name: "modified worktree",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: Module, Version: "v0.1.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs", Value: "git"},
					{Key: "vcs.revision", Value: testRevision},
					{Key: "vcs.modified", Value: "true"},
				},
			},
			want: "modified VCS worktree",
		},
		{
			name: "missing VCS",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: Module, Version: "v0.1.0"},
			},
			want: "no VCS build information",
		},
		{
			name: "missing revision",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: Module, Version: "v0.1.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs", Value: "git"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			want: "no exact VCS revision",
		},
		{
			name: "non-exact main version",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: Module, Version: "0.1.0-dev"},
				Settings:  validSettings,
			},
			want: "not a release, pseudo-version, or development revision",
		},
		{
			name: "dependency without sum",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: "example.com/consumer", Version: "(devel)"},
				Deps:      []*debug.Module{{Path: Module, Version: "v0.2.0"}},
			},
			want: "omits its exact module sum",
		},
		{
			name: "dependency with non-exact version",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: "example.com/consumer", Version: "(devel)"},
				Deps:      []*debug.Module{{Path: Module, Version: "0.2.0", Sum: testSum}},
			},
			want: "not an exact module version",
		},
		{
			name: "dependency with invalid sum",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: "example.com/consumer", Version: "(devel)"},
				Deps:      []*debug.Module{{Path: Module, Version: "v0.2.0", Sum: "h1:not-a-sum"}},
			},
			want: "invalid module sum",
		},
		{
			name: "replacement changes module",
			info: &debug.BuildInfo{
				GoVersion: "go1.26.10",
				Main:      debug.Module{Path: "example.com/consumer", Version: "(devel)"},
				Deps: []*debug.Module{{
					Path:    Module,
					Version: "v0.2.0",
					Sum:     testSum,
					Replace: &debug.Module{Path: "example.com/fork", Version: "v0.3.0", Sum: testSum},
				}},
			},
			want: "replaces module",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := embeddedIdentity(test.info)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestExactModuleVersion(t *testing.T) {
	for _, value := range []string{
		"v0.1.0",
		"v1.2.3-rc.1",
		"v1.2.3-0.20261108181220-c4d2e2a5cdd6",
		"v1.2.3-dev",
		"v0.0.0-20261108181220-c4d2e2a5cdd6",
		"v2.0.0+incompatible",
	} {
		require.True(t, ExactModuleVersion(value), value)
	}
	for _, value := range []string{
		"", "0.1.0", "v1", "v1.2", "v1.2.03", "v1.2.3-", "v1.2.3_dev",
		"v1.2.3-01", "v1.2.3+meta",
	} {
		require.False(t, ExactModuleVersion(value), value)
	}
}

func TestValidModuleSum(t *testing.T) {
	require.True(t, ValidModuleSum(testSum))
	for _, value := range []string{
		"",
		"h1:",
		"h1:not-a-sum",
		"sha256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
		"h1:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU",
		"h1:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFV=",
	} {
		require.False(t, ValidModuleSum(value), value)
	}
}

func TestValidCommitTime(t *testing.T) {
	require.True(t, ValidCommitTime(testTime))
	for _, value := range []string{"", "yesterday", "2026-11-08", "2026-11-08T18:12:20"} {
		require.False(t, ValidCommitTime(value), value)
	}
}

func TestValidRevision(t *testing.T) {
	require.True(t, ValidRevision(testRevision))
	require.True(t, ValidRevision(testRevision+testRevision[:24]))
	for _, value := range []string{
		"",
		testRevision[:39],
		testRevision + "0",
		strings.ToUpper(testRevision),
		testRevision[:39] + "g",
	} {
		require.False(t, ValidRevision(value), value)
	}
}
