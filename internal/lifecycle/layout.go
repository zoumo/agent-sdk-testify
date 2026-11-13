package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
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
	fixturePackages      = "packages"
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

// PackageRoot is the single persistent root for one material and its evidence.
func PackageRoot(fixtureDir, materialID string) string {
	return filepath.Join(fixtureDir, fixturePackages, materialID)
}

func PackageExecutionRoot(fixtureDir, materialID, executionID string) string {
	return filepath.Join(PackageRoot(fixtureDir, materialID), fixtureExecutions, executionID)
}

func PackageReference(fixtureDir, materialID, target string) (string, error) {
	if err := validateExecutionID(materialID); err != nil {
		return "", err
	}
	if _, err := ResolveFixtureReference(fixtureDir, path.Join(fixturePackages, materialID)); err != nil {
		return "", err
	}
	return FixtureReference(PackageRoot(fixtureDir, materialID), target)
}

func ResolvePackageReference(fixtureDir, materialID, reference string) (string, error) {
	if err := validateExecutionID(materialID); err != nil {
		return "", err
	}
	if _, err := ResolveFixtureReference(fixtureDir, path.Join(fixturePackages, materialID)); err != nil {
		return "", err
	}
	return resolveReference(PackageRoot(fixtureDir, materialID), reference, "package")
}

func MaterialRoot(fixtureDir, materialID string) string {
	return filepath.Join(PackageRoot(fixtureDir, materialID), "material")
}

func ExecutionOverlayRoot(fixtureDir, executionID string) string {
	return filepath.Join(fixtureDir, filepath.FromSlash(fixtureRuntime), filepath.FromSlash(fixtureExecutions), executionID)
}

func ExecutionArtifactRoot(fixtureDir, executionID, action string) string {
	if action == "record" {
		action = "capture"
	}
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
	if relative, err := filepath.Rel(root, filepath.Clean(target)); err == nil && filepath.IsLocal(relative) {
		if err := rejectLinkedReference(root, filepath.ToSlash(relative)); err != nil {
			return "", err
		}
	}
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
			for _, part := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, part)
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
	if err := rejectLinkedReference(root, reference); err != nil {
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

func rejectLinkedReference(root, reference string) error {
	current := root
	parts := strings.Split(reference, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("reference traverses a symlink: %q", reference)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("reference traverses a non-directory: %q", reference)
		}
		if i == len(parts)-1 && !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("reference is not an ordinary file or directory: %q", reference)
		}
	}
	return nil
}

func withinResolved(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && (relative == "." || (relative != ".." && filepath.IsLocal(relative)))
}

// MaterialReference derives the fixture-root-relative reference for a material
// identity. Material contents use references relative to this root.
func MaterialReference(materialID string) string {
	return path.Join(fixturePackages, materialID, "material")
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
