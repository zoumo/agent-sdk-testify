// Package version owns the executable identity shared by commands and evidence.
package version

import "runtime/debug"

const Module = "github.com/zoumo/agent-sdk-testify"

// buildVersion is populated from the exact Git tag by release and build tools.
// Module installations instead use the version recorded in Go build info.
var buildVersion string

// String returns the tagged executable version, or "devel" outside a release.
func String() string {
	if buildVersion != "" {
		return buildVersion
	}
	info, ok := debug.ReadBuildInfo()
	return versionFromBuildInfo(info, ok)
}

func versionFromBuildInfo(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return "devel"
	}
	module, _ := embeddedModule(info)
	if module == nil {
		return "devel"
	}
	selected := module
	if module.Replace != nil {
		selected = module.Replace
	}
	if !ExactModuleVersion(selected.Version) {
		return "devel"
	}
	return selected.Version
}
