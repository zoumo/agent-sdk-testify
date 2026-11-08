package version

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// BuildIdentity is the exact source identity of the embedded library.
type BuildIdentity struct {
	Module    string
	Version   string
	Sum       string
	Revision  string
	Time      string
	GoVersion string
}

// ReadEmbeddedIdentity returns the source identity recorded by Go build info.
func ReadEmbeddedIdentity() (BuildIdentity, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return BuildIdentity{}, errors.New("embedded tool identity is unavailable: build info is absent")
	}
	return embeddedIdentity(info)
}

func embeddedIdentity(info *debug.BuildInfo) (BuildIdentity, error) {
	if info == nil {
		return BuildIdentity{}, errors.New("embedded tool identity is unavailable: build info is absent")
	}
	module, main := embeddedModule(info)
	if module == nil {
		return BuildIdentity{}, fmt.Errorf("embedded tool identity does not contain module %s", Module)
	}
	selected := module
	if module.Replace != nil {
		if module.Replace.Path != Module {
			return BuildIdentity{}, fmt.Errorf("embedded tool identity replaces module %s with %s", Module, module.Replace.Path)
		}
		selected = module.Replace
	}

	identity := BuildIdentity{
		Module:    Module,
		Version:   selected.Version,
		Sum:       selected.Sum,
		GoVersion: info.GoVersion,
	}
	if identity.GoVersion == "" {
		return BuildIdentity{}, errors.New("embedded tool identity omits the Go version")
	}
	if identity.Sum != "" && !ValidModuleSum(identity.Sum) {
		return BuildIdentity{}, errors.New("embedded tool identity has an invalid module sum")
	}

	if main {
		settings := make(map[string]string, len(info.Settings))
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
		}
		identity.Revision = settings["vcs.revision"]
		if settings["vcs"] == "" {
			return BuildIdentity{}, errors.New("embedded tool identity has no VCS build information")
		}
		if !ValidRevision(identity.Revision) {
			return BuildIdentity{}, errors.New("embedded tool identity has no exact VCS revision")
		}
		identity.Time = settings["vcs.time"]
		if identity.Time != "" && !ValidCommitTime(identity.Time) {
			return BuildIdentity{}, errors.New("embedded tool identity has an invalid VCS commit time")
		}
		if settings["vcs.modified"] != "false" {
			return BuildIdentity{}, errors.New("embedded tool identity reports a modified VCS worktree")
		}
		if identity.Version != "(devel)" && !ExactModuleVersion(identity.Version) {
			return BuildIdentity{}, fmt.Errorf("embedded tool version %q is not a release, pseudo-version, or development revision", identity.Version)
		}
		return identity, nil
	}

	if !ExactModuleVersion(identity.Version) {
		return BuildIdentity{}, fmt.Errorf("embedded tool version %q is not an exact module version", identity.Version)
	}
	if !ValidModuleSum(identity.Sum) {
		return BuildIdentity{}, errors.New("embedded tool identity omits its exact module sum")
	}
	return identity, nil
}

func embeddedModule(info *debug.BuildInfo) (*debug.Module, bool) {
	if info.Main.Path == Module {
		return &info.Main, true
	}
	for _, dependency := range info.Deps {
		if dependency != nil && dependency.Path == Module {
			return dependency, false
		}
	}
	return nil, false
}

// ExactModuleVersion reports whether value is a release or pseudo-version
// accepted by the Go module version grammar.
func ExactModuleVersion(value string) bool {
	if !strings.HasPrefix(value, "v") {
		return false
	}
	value = strings.TrimPrefix(value, "v")
	if build := strings.IndexByte(value, '+'); build >= 0 {
		if value[build+1:] != "incompatible" {
			return false
		}
		value = value[:build]
	}
	core, prerelease, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if !canonicalNumber(part) {
			return false
		}
	}
	return !hasPrerelease || validPrerelease(prerelease)
}

// ValidRevision reports whether value is a full Git object revision.
func ValidRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// ValidCommitTime reports whether value is an RFC 3339 VCS commit timestamp.
func ValidCommitTime(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

// ValidModuleSum reports whether value is an exact h1 module content sum.
func ValidModuleSum(value string) bool {
	if !strings.HasPrefix(value, "h1:") {
		return false
	}
	digest, err := base64.StdEncoding.DecodeString(value[len("h1:"):])
	if err != nil || len(digest) != sha256.Size {
		return false
	}
	return base64.StdEncoding.EncodeToString(digest) == value[len("h1:"):]
}

func canonicalNumber(value string) bool {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validPrerelease(value string) bool {
	if value == "" {
		return false
	}
	for identifier := range strings.SplitSeq(value, ".") {
		if identifier == "" || (numericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0') {
			return false
		}
		for _, char := range identifier {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && char != '-' {
				return false
			}
		}
	}
	return true
}

func numericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
