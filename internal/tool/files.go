package tool

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func CopyFile(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

func CopyTree(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source is not an unlinked directory: %s", source)
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is forbidden: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := target
		if rel != "." {
			destination = filepath.Join(target, rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		return CopyFile(path, destination)
	})
}

func WriteFiles(root string, files Object) error {
	for name, value := range files {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("material path escapes workspace: %s", name)
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(Str(value)), 0o600); err != nil {
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
