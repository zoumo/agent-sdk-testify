package version

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersionFromBuildInfo(t *testing.T) {
	for _, test := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{
			name: "main module",
			info: &debug.BuildInfo{Main: debug.Module{Path: Module, Version: "v0.1.0"}},
			ok:   true,
			want: "v0.1.0",
		},
		{
			name: "library dependency",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/consumer", Version: "(devel)"},
				Deps: []*debug.Module{{Path: Module, Version: "v0.1.0"}},
			},
			ok:   true,
			want: "v0.1.0",
		},
		{
			name: "replaced dependency",
			info: &debug.BuildInfo{
				Main: debug.Module{Path: "example.com/consumer", Version: "v1.0.0"},
				Deps: []*debug.Module{{
					Path:    Module,
					Version: "v0.1.0",
					Replace: &debug.Module{Path: Module, Version: "v0.2.0"},
				}},
			},
			ok:   true,
			want: "v0.2.0",
		},
		{
			name: "development module",
			info: &debug.BuildInfo{Main: debug.Module{Path: Module, Version: "(devel)"}},
			ok:   true,
			want: "devel",
		},
		{
			name: "absent module",
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/consumer", Version: "v1.0.0"}},
			ok:   true,
			want: "devel",
		},
		{
			name: "build info unavailable",
			want: "devel",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, versionFromBuildInfo(test.info, test.ok))
		})
	}
}
