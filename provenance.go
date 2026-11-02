package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Check source bytes, not runtime identities: current drivers, SDK versions and
// peer binaries remain freely selectable. Shared/synthetic materials need no
// recording manifest. Existing manifests, when present, must remain truthful.
func verifyMaterials(root string, requireComplete bool) error {
	for _, phase := range []string{"", "remote-probe"} {
		if err := verifySourcePhase(filepath.Join(root, phase), requireComplete); err != nil {
			return err
		}
	}
	if requireComplete {
		var service object
		if err := readJSON(filepath.Join(root, "remote-service.json"), &service); err == nil {
			if service["cleanupComplete"] != true || service["serverExitCode"] != float64(0) || service["forcedCleanup"] == true {
				return fmt.Errorf("source remote service cleanup is incomplete")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func verifySourcePhase(root string, requireComplete bool) error {
	for _, name := range []string{"manifest.json", "raw/manifest.json"} {
		var m object
		if err := readJSON(filepath.Join(root, name), &m); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if requireComplete && str(m["captureStatus"]) != "" && m["captureStatus"] != "complete" {
			return fmt.Errorf("source recording capture is %s", str(m["captureStatus"]))
		}
		if name == "manifest.json" {
			if digest := str(m["planSHA256"]); digest != "" {
				if err := verifyFile(root, "plan.json", digest); err != nil {
					return err
				}
			}
			for _, group := range []string{"savedFiles", "caseFiles", "remotePhaseFiles", "driverFiles"} {
				for path, digest := range obj(m[group]) {
					if err := verifyFile(root, path, str(digest)); err != nil {
						return err
					}
				}
			}
		}
		for path, digest := range obj(m["files"]) {
			if err := verifyFile(filepath.Join(root, "raw"), path, str(digest)); err != nil {
				return err
			}
		}
	}
	if requireComplete {
		var transport object
		if err := readJSON(filepath.Join(root, "raw/transport.json"), &transport); err == nil {
			if transport["captureStatus"] != "complete" || transport["cleanupComplete"] != true {
				return fmt.Errorf("source recording capture or cleanup is incomplete")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func verifyFile(root, path, digest string) error {
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
func writeSourceManifest(root string, outcome result) error {
	files, saved, remote := object{}, object{}, object{}
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
	for _, dir := range []string{"config/projects", "config/sessions"} {
		if _, e := os.Stat(filepath.Join(root, dir)); os.IsNotExist(e) {
			continue
		}
		if err = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() && filepath.Ext(p) == ".jsonl" {
				rel, e := filepath.Rel(root, p)
				if e != nil {
					return e
				}
				digest, e := hash(p)
				if e != nil {
					return e
				}
				saved[rel] = digest
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if _, e := os.Stat(filepath.Join(root, "remote-probe/raw")); e == nil {
		entries, e := os.ReadDir(filepath.Join(root, "remote-probe/raw"))
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				name := filepath.Join("remote-probe/raw", entry.Name())
				digest, e := hash(filepath.Join(root, name))
				if e != nil {
					return e
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
	var transport object
	if err = readJSON(filepath.Join(root, "raw/transport.json"), &transport); err != nil {
		return err
	}
	scan, scanErr := scanCaptureCredentials(root)
	manifestErr := writeJSON(filepath.Join(root, "manifest.json"), object{"provenance": "capture", "planSHA256": digest, "files": files, "savedFiles": saved, "remotePhaseFiles": remote, "status": outcome.Status, "captureStatus": transport["captureStatus"], "credentialScan": scan})
	return errors.Join(scanErr, manifestErr)
}
