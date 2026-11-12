package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	fixtureRuntime       = "runtime"
	fixtureBaseline      = "runtime/baseline"
	fixtureRoutes        = "runtime/routes"
	fixtureTemplates     = "runtime/templates"
	fixtureLaunchers     = "runtime/launchers"
	fixturePrivateConfig = "runtime/private-config"
	fixtureToolBinding   = "runtime/tool.json"
	fixtureSourcePlan    = "source/plan.json"
	fixtureSourceSeed    = "source/history-seed"
	fixtureMaterials     = "materials"
	fixtureExecutions    = "executions"
)

func ResourceRoot(fixtureDir string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureRuntime))
}

func BaselineRoot(fixtureDir string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureBaseline))
}

func BaselineWorkspace(fixtureDir string) string {
	return filepath.Join(BaselineRoot(fixtureDir), "workspace")
}

func RouteRoot(fixtureDir, action string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureRoutes), action)
}

func RouteWorkspace(fixtureDir, action string) string {
	return filepath.Join(RouteRoot(fixtureDir, action), "workspace")
}

func TemplateRoot(fixtureDir, action string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureTemplates), action)
}

func PrivateConfigRoot(fixtureDir string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixturePrivateConfig))
}

func ToolBindingPath(fixtureDir string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureToolBinding))
}

func LauncherPath(fixtureDir, connection string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureLaunchers), "peer-"+connection)
}

func MaterialRoot(fixtureDir, materialID string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureMaterials), materialID)
}

func ExecutionOverlayRoot(fixtureDir, executionID string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureRuntime), filepath.FromSlash(fixtureExecutions), executionID)
}

func ExecutionArtifactRoot(fixtureDir, executionID, action string) string {
	return filepath.Join(ExecutionDir(fixtureDir, executionID), action)
}

func SourcePlanPath(fixtureDir string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureSourcePlan))
}

func SourceSeedPath(fixtureDir string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureSourceSeed))
}

// canonicalReference accepts only clean slash-separated paths local to the
// reference base. It deliberately has no notion of placeholders or locators.
func canonicalReference(reference string) error {
	if reference == "" {
		return errors.New("reference is empty")
	}
	if strings.Contains(reference, "\\") || strings.Contains(reference, "\x00") {
		return fmt.Errorf("reference is not slash-separated: %q", reference)
	}
	if path.IsAbs(reference) || filepath.IsAbs(reference) {
		return fmt.Errorf("reference is absolute: %q", reference)
	}
	if reference != path.Clean(reference) || reference == "." {
		return fmt.Errorf("reference is not canonical: %q", reference)
	}
	if !filepath.IsLocal(filepath.FromSlash(reference)) {
		return fmt.Errorf("reference escapes its root: %q", reference)
	}
	return nil
}

// FixtureReference converts an absolute path inside the fixture into a
// canonical fixture-root-relative reference.
func FixtureReference(fixtureDir, target string) (string, error) {
	if !filepath.IsAbs(target) {
		return "", fmt.Errorf("path must be absolute: %q", target)
	}
	root := filepath.Clean(fixtureDir)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	existing := filepath.Clean(target)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			if !withinResolved(resolvedRoot, resolved) {
				return "", fmt.Errorf("path escapes fixture through a symlink: %q", target)
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			relative, err := filepath.Rel(resolvedRoot, resolved)
			if err != nil {
				return "", err
			}
			reference := filepath.ToSlash(relative)
			if reference == "." {
				return "", fmt.Errorf("path escapes fixture: %q", target)
			}
			if err := canonicalReference(reference); err != nil {
				return "", err
			}
			return reference, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		missing = append(missing, filepath.Base(existing))
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("fixture root is unavailable: %q", fixtureDir)
		}
		existing = parent
	}
}

// ResolveFixtureReference resolves a fixture-root-relative reference and
// rejects absolute, non-clean and symlink-escaping values.
func ResolveFixtureReference(fixtureDir, reference string) (string, error) {
	return resolveReference(fixtureDir, reference, "fixture")
}

func resolveReference(root, reference, kind string) (string, error) {
	if err := canonicalReference(reference); err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(reference))
	existing := target
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			if !withinResolved(resolvedRoot, resolved) {
				return "", fmt.Errorf("%s reference escapes through a symlink: %q", kind, reference)
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("%s reference root is unavailable: %q", kind, reference)
		}
		existing = parent
	}
	return target, nil
}

func withinResolved(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && (relative == "." || (relative != ".." && filepath.IsLocal(relative)))
}

// MaterialReference derives the fixture-root-relative reference for a material
// identity. Material contents use references relative to this root.
func MaterialReference(materialID string) string {
	return path.Join(fixtureMaterials, materialID)
}

// stripFixtureArgs removes the single fixture-owned argument pair from a
// recorded action argv. Other arguments, including absolute native paths, are
// recorded facts and remain intact.
func stripFixtureArgs(fixtureDir string, args []string) ([]string, error) {
	if len(args) < 2 || args[0] != "--fixture-dir" || args[1] != fixtureDir {
		return nil, errors.New("execution arguments omit their fixture root")
	}
	return append([]string(nil), args[2:]...), nil
}

func withFixtureArgs(fixtureDir string, args []string) []string {
	restored := make([]string, 0, len(args)+2)
	restored = append(restored, "--fixture-dir", fixtureDir)
	return append(restored, args...)
}

// normalizeFixtureArgs rewrites the fixture-owned argv pair to the canonical
// fixture root when the caller supplied an equivalent spelling of that root.
func normalizeFixtureArgs(fixtureDir string, args []string) []string {
	if len(args) < 2 || args[0] != "--fixture-dir" || args[1] == fixtureDir {
		return args
	}
	supplied, err := filepath.EvalSymlinks(args[1])
	if err != nil {
		return args
	}
	canonical, err := filepath.EvalSymlinks(fixtureDir)
	if err != nil || supplied != canonical {
		return args
	}
	normalized := append([]string(nil), args...)
	normalized[1] = fixtureDir
	return normalized
}
