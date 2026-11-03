package history

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

var historyRoots = []string{"config/projects", "config/sessions", "history-seed"}

// Bindings validates the selected material's complete history inventory and
// returns the manifest-bound relative paths.
func Bindings(root string) (map[string]string, error) {
	var manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest); err != nil {
		return nil, err
	}
	bindings, err := manifestBindings(root, tool.Obj(manifest["savedFiles"]))
	if err != nil {
		return nil, err
	}
	if err := checkUnbound(root, bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

func manifestBindings(root string, saved tool.Object) (map[string]string, error) {
	bindings := make(map[string]string, len(saved))
	for name, value := range saved {
		if name == "" || name != path.Clean(name) || !filepath.IsLocal(filepath.FromSlash(name)) {
			return nil, fmt.Errorf("history path escapes material: %s", name)
		}
		if !allowed(name) {
			return nil, fmt.Errorf("history path is outside allowed history: %s", name)
		}
		digest, ok := value.(string)
		if !ok || !validDigest(digest) {
			return nil, fmt.Errorf("history omits a valid SHA-256 binding: %s", name)
		}
		if err := checkFile(root, name, digest); err != nil {
			return nil, err
		}
		bindings[name] = digest
	}
	return bindings, nil
}

func checkUnbound(root string, bindings map[string]string) error {
	for _, prefix := range historyRoots {
		base := filepath.Join(root, filepath.FromSlash(prefix))
		info, err := os.Lstat(base)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("history root is not an unlinked directory: %s", prefix)
		}
		if err := filepath.WalkDir(base, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("history symlink is forbidden: %s", current)
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("history entry is not a regular file: %s", current)
			}
			rel, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(rel)
			if _, ok := bindings[name]; !ok {
				return fmt.Errorf("unbound history file: %s", name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func checkFile(root, name, expected string) error {
	if !allowed(name) {
		return fmt.Errorf("history path is outside allowed history: %s", name)
	}
	return checkLocalFile(root, name, expected)
}

func checkLocalFile(root, name, expected string) error {
	if name == "" || name != path.Clean(name) || !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("path escapes material: %s", name)
	}
	current := root
	parts := split(name)
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("history symlink is forbidden: %s", name)
		}
		if i == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("history entry is not a regular file: %s", name)
			}
		} else if !info.IsDir() {
			return fmt.Errorf("history parent is not a directory: %s", name)
		}
	}
	actual, err := digest(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("history hash mismatch: %s", name)
	}
	return nil
}

func HashTree(root string) (map[string]string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("history root is not an unlinked directory")
	}
	bindings := map[string]string{}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("history symlink is forbidden: %s", current)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("history entry is not a regular file: %s", current)
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		value, err := digest(current)
		if err != nil {
			return err
		}
		bindings[filepath.ToSlash(rel)] = value
		return nil
	})
	return bindings, err
}

func VerifyTree(root string, bindings map[string]string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("history root is not an unlinked directory")
	}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := checkLocalFile(root, name, bindings[name]); err != nil {
			return err
		}
	}
	return nil
}

// Materialize copies only manifest-bound history files and verifies both the
// source and destination bytes.
func Materialize(source, target string) (map[string]string, error) {
	bindings, err := Bindings(source)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := copyVerified(filepath.Join(source, filepath.FromSlash(name)), filepath.Join(target, filepath.FromSlash(name)), bindings[name]); err != nil {
			return nil, err
		}
	}
	if err := Check(target, bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

// Check verifies the exact files recorded in an execution descriptor without
// treating peer-created runtime files as source history.
func Check(root string, bindings map[string]string) error {
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := checkFile(root, name, bindings[name]); err != nil {
			return err
		}
	}
	return nil
}

func ValidateBindings(bindings map[string]string) error {
	for name, digest := range bindings {
		if !allowed(name) {
			return fmt.Errorf("history path is outside allowed history: %s", name)
		}
		if !validDigest(digest) {
			return fmt.Errorf("history omits a valid SHA-256 binding: %s", name)
		}
	}
	return nil
}

func Reset(root string) error {
	for _, prefix := range historyRoots {
		current := filepath.Join(root, filepath.FromSlash(prefix))
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("history root is not an unlinked directory: %s", prefix)
		}
		if err := os.RemoveAll(current); err != nil {
			return err
		}
	}
	return nil
}

func allowed(name string) bool {
	if name == "" || name != path.Clean(name) || !filepath.IsLocal(filepath.FromSlash(name)) {
		return false
	}
	for _, prefix := range historyRoots {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix && name[len(prefix)] == '/' {
			return true
		}
	}
	return false
}

func split(name string) []string {
	var parts []string
	for {
		part, rest, found := cut(name, '/')
		parts = append(parts, part)
		if !found {
			return parts
		}
		name = rest
	}
}

func cut(value string, separator byte) (string, string, bool) {
	for i := 0; i < len(value); i++ {
		if value[i] == separator {
			return value[:i], value[i+1:], true
		}
	}
	return value, "", false
}

func copyVerified(source, target, expected string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	closeErr := out.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(target)
		return err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		_ = os.Remove(target)
		return fmt.Errorf("history hash mismatch: %s", filepath.ToSlash(target))
	}
	return nil
}

func digest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
