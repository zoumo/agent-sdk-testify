package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
)

// Export copies the exact current package without creating an execution or
// changing session state.
func Export(sessionDir, output string) error {
	dir, err := canonicalSessionDir(sessionDir)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(output) {
		return errors.New("--output must be an absolute path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return err
	}
	target := filepath.Join(parent, filepath.Base(output))
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return err
	}
	if rel == "." || filepath.IsLocal(rel) {
		return errors.New("--output must be outside session")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		if err == nil {
			return fmt.Errorf("--output already exists: %s", target)
		}
		return err
	}
	// Use the existing lock file, without creating anything in the session.
	// An exclusive lock also excludes mock connections that hold shared locks.
	file, err := os.OpenFile(filepath.Join(dir, "locks", "action.lock"), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	lock, err := lockExport(file)
	if err != nil {
		return err
	}
	defer lock.Close()
	session, err := readSessionUnlocked(dir)
	if err != nil {
		return err
	}
	material, err := LatestArtifact(dir, session)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".agent-sdk-testify-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := copyMaterial(material.Path, stage); err != nil {
		return err
	}
	if err := corpus.CheckMaterialInventory(stage, *material); err != nil {
		return err
	}
	if err := corpus.VerifyMaterials(stage, true); err != nil {
		return err
	}
	// Recheck the selected source against its registry after copying. This also
	// rejects out-of-band tampering that occurred while reading the package.
	if _, err := inspectMaterial(session, *material); err != nil {
		return err
	}
	return renameExport(stage, target)
}
