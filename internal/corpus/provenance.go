package corpus

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Check source bytes, not runtime identities: current drivers, SDK versions and
// peer binaries remain freely selectable. Shared/synthetic materials need no
// recording manifest. Existing manifests, when present, must remain truthful.
func VerifyMaterials(root string, requireComplete bool) error {
	for _, phase := range []string{"", "remote-probe"} {
		if err := VerifySourcePhase(filepath.Join(root, phase), requireComplete); err != nil {
			return err
		}
	}
	if requireComplete {
		var service tool.Object
		if err := tool.ReadJSON(filepath.Join(root, "remote-service.json"), &service); err == nil {
			if service["cleanupComplete"] != true || service["serverExitCode"] != float64(0) || service["forcedCleanup"] == true {
				return errors.New("source remote service cleanup is incomplete")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func VerifySourcePhase(root string, requireComplete bool) error {
	for _, name := range []string{"manifest.json", "raw/manifest.json"} {
		var m tool.Object
		if err := tool.ReadJSON(filepath.Join(root, name), &m); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if requireComplete && tool.Str(m["captureStatus"]) != "" && m["captureStatus"] != "complete" {
			return fmt.Errorf("source recording capture is %s", tool.Str(m["captureStatus"]))
		}
		if name == "manifest.json" {
			if digest := tool.Str(m["planSHA256"]); digest != "" {
				if err := VerifyFile(root, "plan.json", digest); err != nil {
					return err
				}
			}
			for _, group := range []string{"savedFiles", "caseFiles", "remotePhaseFiles", "driverFiles"} {
				for path, digest := range tool.Obj(m[group]) {
					if err := VerifyFile(root, path, tool.Str(digest)); err != nil {
						return err
					}
				}
			}
		}
		for path, digest := range tool.Obj(m["files"]) {
			if err := VerifyFile(filepath.Join(root, "raw"), path, tool.Str(digest)); err != nil {
				return err
			}
		}
	}
	if requireComplete {
		var transport tool.Object
		if err := tool.ReadJSON(filepath.Join(root, "raw", "transport.json"), &transport); err == nil {
			if transport["captureStatus"] != "complete" || transport["cleanupComplete"] != true {
				return errors.New("source recording capture or cleanup is incomplete")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func VerifyFile(root, path, digest string) error {
	if !filepath.IsLocal(path) {
		return fmt.Errorf("source path escapes material directory: %s", path)
	}
	info, err := os.Lstat(filepath.Join(root, path))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", path)
	}
	b, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != digest {
		return fmt.Errorf("source hash mismatch: %s", path)
	}
	return nil
}

// New captures are reusable as materials; hashes describe source bytes, never
// the versions or binary identities selected for a later run.
func WriteSourceManifest(root string, outcome tool.Result) error {
	files, saved, remote := tool.Object{}, tool.Object{}, tool.Object{}
	hash := func(path string) (string, error) {
		b, e := os.ReadFile(path)
		if e != nil {
			return "", e
		}
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:]), nil
	}
	entries, err := os.ReadDir(filepath.Join(root, "raw"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			digest, e := hash(filepath.Join(root, "raw", entry.Name()))
			if e != nil {
				return e
			}
			files[entry.Name()] = digest
		}
	}
	for _, dir := range []string{"config/projects", "config/sessions", "history-seed"} {
		if _, e := os.Stat(filepath.Join(root, dir)); os.IsNotExist(e) {
			continue
		}
		if err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("source history symlink is forbidden: %s", p)
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("source history is not a regular file: %s", p)
			}
			rel, e := filepath.Rel(root, p)
			if e != nil {
				return e
			}
			digest, e := hash(p)
			if e != nil {
				return e
			}
			saved[filepath.ToSlash(rel)] = digest
			return nil
		}); err != nil {
			return err
		}
	}
	if _, e := os.Stat(filepath.Join(root, "remote-probe", "raw")); e == nil {
		entries, e := os.ReadDir(filepath.Join(root, "remote-probe", "raw"))
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				name := filepath.Join("remote-probe", "raw", entry.Name())
				digest, hashErr := hash(filepath.Join(root, name))
				if hashErr != nil {
					return hashErr
				}
				remote[name] = digest
			}
		}
		digest, e := hash(filepath.Join(root, "remote-service.json"))
		if e != nil {
			return e
		}
		remote["remote-service.json"] = digest
	}
	digest, err := hash(filepath.Join(root, "plan.json"))
	if err != nil {
		return err
	}
	var transport tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "raw", "transport.json"), &transport); err != nil {
		return err
	}
	scan, scanErr := ScanCaptureCredentials(root)
	manifestErr := tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{"provenance": "capture", "planSHA256": digest, "files": files, "savedFiles": saved, "remotePhaseFiles": remote, "status": outcome.Status, "captureStatus": transport["captureStatus"], "credentialScan": scan})
	return errors.Join(scanErr, manifestErr)
}
