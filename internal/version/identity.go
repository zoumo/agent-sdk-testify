package version

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	goversion "go/version"
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

// EmbeddedIdentity is the exact module identity supplied by a library consumer
// when the current test binary omits this module from its build dependencies.
type EmbeddedIdentity struct {
	Version   string
	Sum       string
	Revision  string
	Time      string
	GoVersion string
}

// ReadEmbeddedIdentity returns the source identity recorded by Go build info.
func ReadEmbeddedIdentity() (BuildIdentity, error) {
	return ResolveEmbeddedIdentity(nil)
}

// ResolveEmbeddedIdentity reads build information and uses provided only when
// the module is absent. A self-proven identity remains authoritative.
func ResolveEmbeddedIdentity(provided *EmbeddedIdentity) (BuildIdentity, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return BuildIdentity{}, errors.New("embedded tool identity is unavailable: build info is absent")
	}
	return resolveEmbeddedIdentity(info, provided)
}

func resolveEmbeddedIdentity(info *debug.BuildInfo, provided *EmbeddedIdentity) (BuildIdentity, error) {
	if info == nil {
		return BuildIdentity{}, errors.New("embedded tool identity is unavailable: build info is absent")
	}
	var supplied *BuildIdentity
	if provided != nil {
		value, err := providedEmbeddedIdentity(info, provided)
		if err != nil {
			return BuildIdentity{}, err
		}
		supplied = &value
	}
	module, _ := embeddedModule(info)
	if module == nil {
		if supplied == nil {
			return embeddedIdentity(info)
		}
		return *supplied, nil
	}
	proven, err := embeddedIdentity(info)
	if err != nil {
		return BuildIdentity{}, err
	}
	if supplied != nil {
		if err := compareEmbeddedIdentities(*supplied, proven); err != nil {
			return BuildIdentity{}, err
		}
	}
	return proven, nil
}

func providedEmbeddedIdentity(info *debug.BuildInfo, provided *EmbeddedIdentity) (BuildIdentity, error) {
	if info.GoVersion == "" {
		return BuildIdentity{}, errors.New("explicit embedded identity requires build Go version")
	}
	if provided.GoVersion == "" {
		return BuildIdentity{}, errors.New("explicit embedded identity requires the Go version")
	}
	if !goversion.IsValid(provided.GoVersion) {
		return BuildIdentity{}, errors.New("explicit embedded identity has an invalid Go version")
	}
	if provided.GoVersion != info.GoVersion {
		return BuildIdentity{}, fmt.Errorf(
			"explicit embedded identity Go version %q differs from build Go version %q",
			provided.GoVersion, info.GoVersion,
		)
	}
	if !ExactModuleVersion(provided.Version) {
		return BuildIdentity{}, fmt.Errorf("explicit embedded identity version %q is not exact", provided.Version)
	}
	if !ValidModuleSum(provided.Sum) {
		return BuildIdentity{}, errors.New("explicit embedded identity requires an exact h1 module sum")
	}
	if !ValidRevision(provided.Revision) {
		return BuildIdentity{}, errors.New("explicit embedded identity requires a full 40- or 64-character lowercase revision")
	}
	if !ValidCommitTime(provided.Time) {
		return BuildIdentity{}, errors.New("explicit embedded identity requires an RFC 3339 commit time")
	}
	if err := validatePseudoVersion(provided.Version, provided.Revision, provided.Time); err != nil {
		return BuildIdentity{}, err
	}
	return BuildIdentity{
		Module:    Module,
		Version:   provided.Version,
		Sum:       provided.Sum,
		Revision:  provided.Revision,
		Time:      provided.Time,
		GoVersion: provided.GoVersion,
	}, nil
}

func compareEmbeddedIdentities(provided, proven BuildIdentity) error {
	for _, field := range []struct {
		name     string
		provided string
		proven   string
	}{
		{name: "module", provided: provided.Module, proven: proven.Module},
		{name: "version", provided: provided.Version, proven: proven.Version},
		{name: "module sum", provided: provided.Sum, proven: proven.Sum},
		{name: "revision", provided: provided.Revision, proven: proven.Revision},
		{name: "commit time", provided: provided.Time, proven: proven.Time},
		{name: "Go version", provided: provided.GoVersion, proven: proven.GoVersion},
	} {
		if field.proven != "" && field.provided != field.proven {
			return fmt.Errorf(
				"explicit embedded identity %s %q conflicts with build information %q",
				field.name, field.provided, field.proven,
			)
		}
	}
	return nil
}

func validatePseudoVersion(value, revision, commitTime string) error {
	timestampText, shortRevision, ok := pseudoVersionParts(value)
	if !ok {
		return nil
	}
	timestamp, err := time.ParseInLocation("20060102150405", timestampText, time.UTC)
	if err != nil {
		return errors.New("explicit embedded identity has an invalid pseudo-version timestamp")
	}
	committed, err := time.Parse(time.RFC3339, commitTime)
	if err != nil {
		return errors.New("explicit embedded identity requires an RFC 3339 commit time")
	}
	if !timestamp.Equal(committed.UTC().Truncate(time.Second)) {
		return errors.New("explicit embedded identity pseudo-version timestamp differs from commit time")
	}
	if !strings.HasPrefix(revision, shortRevision) {
		return errors.New("explicit embedded identity pseudo-version revision differs from commit revision")
	}
	return nil
}

func pseudoVersionParts(value string) (timestamp, revision string, ok bool) {
	revisionAt := strings.LastIndexByte(value, '-')
	if revisionAt < 0 {
		return "", "", false
	}
	revision = value[revisionAt+1:]
	if len(revision) != 12 || !lowerHex(revision) {
		return "", "", false
	}
	prefix := value[:revisionAt]
	timestampAt := strings.LastIndexByte(prefix, '-')
	if timestampAt < 0 {
		return "", "", false
	}
	timestamp = prefix[timestampAt+1:]
	if len(timestamp) != 14 || !decimal(timestamp) {
		return "", "", false
	}
	return timestamp, revision, true
}

func lowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func decimal(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
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
