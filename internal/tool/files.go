package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

func CopyFile(source, target string) error {
	b, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	return os.WriteFile(target, b, 0600)
}

func CopyTree(source, target string) error {
	return filepath.WalkDir(source, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("material is not a regular file: %s", p)
		}
		return CopyFile(p, dest)
	})
}

func WriteFiles(root string, files Object) error {
	for name, value := range files {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("material path escapes workspace: %s", name)
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(Str(value)), 0600); err != nil {
			return err
		}
	}
	return nil
}

func CopySaved(source, target string) error {
	for _, name := range []string{"config/projects", "config/sessions", "history-seed"} {
		p := filepath.Join(source, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := CopyTree(p, filepath.Join(target, name)); err != nil {
			return err
		}
	}
	return nil
}
