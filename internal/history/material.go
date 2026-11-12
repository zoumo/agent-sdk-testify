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
	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "plan.json"), &plan); err != nil {
		return nil, err
	}
	bindings := map[string]string{}
	if err := addBindings(root, bindings, "manifest.savedFiles", tool.Obj(manifest["savedFiles"]), func(value any) string {
		digest, _ := value.(string)
		return digest
	}); err != nil {
		return nil, err
	}
	if err := addBindings(root, bindings, "plan.historySeed.files", tool.Obj(tool.Obj(plan["historySeed"])["files"]), func(value any) string {
		return tool.Str(tool.Obj(value)["sha256"])
	}); err != nil {
		return nil, err
	}
	seed := tool.Obj(plan["historySeed"])
	if source := tool.Str(seed["sourceRootRef"]); source != "" {
		sourceBindings := tool.Object{}
		for name, digest := range tool.Obj(seed["sourceFiles"]) {
			sourceBindings[path.Join(source, name)] = digest
		}
		if err := addBindings(root, bindings, "plan.historySeed.sourceFiles", sourceBindings, tool.Str); err != nil {
			return nil, err
		}
	}
	if seed["projectSnapshot"] == "history-seed/project" {
		projectBindings := tool.Object{}
		for name, value := range tool.Obj(seed["projectFiles"]) {
			if value != "directory" {
				projectBindings["history-seed/project/"+name] = value
			}
		}
		if err := addBindings(root, bindings, "plan.historySeed.projectFiles", projectBindings, tool.Str); err != nil {
			return nil, err
		}
	}
	var adoption tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "adoption.json"), &adoption); err == nil {
		transformations, transformationsOK := adoption["transformations"].([]any)
		files, filesOK := adoption["files"].(map[string]any)
		if adoption["adoption"] != "admitted" || !transformationsOK || len(transformations) != 0 || !filesOK {
			return nil, errors.New("unsupported adopted history evidence")
		}
		historyFiles := tool.Object{}
		for name, value := range files {
			if allowed(name) {
				historyFiles[name] = value
			}
		}
		if err := addBindings(root, bindings, "adoption.files", historyFiles, func(value any) string {
			digest, _ := value.(string)
			return digest
		}); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := checkUnbound(root, bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

func addBindings(root string, bindings map[string]string, source string, values tool.Object, digestOf func(any) string) error {
	for name, value := range values {
		if name == "" || name != path.Clean(name) || !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("history path escapes material: %s", name)
		}
		if !allowed(name) {
			return fmt.Errorf("history path is outside allowed history in %s: %s", source, name)
		}
		digest := digestOf(value)
		if !validDigest(digest) {
			return fmt.Errorf("history omits a valid SHA-256 binding in %s: %s", source, name)
		}
		if existing, ok := bindings[name]; ok && existing != digest {
			return fmt.Errorf("history bindings conflict for %s", name)
		}
		if err := checkFile(root, name, digest); err != nil {
			return err
		}
		bindings[name] = digest
	}
	return nil
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

// Materialize copies only independently bound history files and verifies both
// the source and destination bytes.
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
	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(source, "plan.json"), &plan); err != nil {
		return nil, err
	}
	if plan["provider"] == "claude" || plan["agentType"] == "claude" {
		if err := MaterializeClaude(source, target, filepath.Join(target, "workspace"), plan); err != nil {
			return nil, err
		}
		var materialization tool.Object
		if err := tool.ReadJSON(filepath.Join(target, "materialization.json"), &materialization); err != nil {
			return nil, err
		}
		for _, item := range tool.Array(materialization["transformations"]) {
			transformation := tool.Obj(item)
			bindings[tool.Str(transformation["derived"])] = tool.Str(transformation["derivedSHA256"])
		}
		if err := Check(target, bindings); err != nil {
			return nil, err
		}
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
	for i := range len(value) {
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
