// Package builtin reads the generated material snapshot embedded in the
// installed binary. Catalog, manifest and archive bytes remain a closed set.
package builtin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type Manifest struct {
	SchemaVersion  int               `json:"schemaVersion"`
	SourceRevision string            `json:"sourceRevision"`
	Scope          string            `json:"scope"`
	IndexSHA256    string            `json:"indexSHA256"`
	Catalog        map[string]string `json:"catalog"`
	ArchiveSHA256  string            `json:"archiveSHA256"`
	Coverage       []tool.Object     `json:"coverage"`
	Materials      []Material        `json:"materials"`
}

type Selection struct {
	ManifestSHA256 string    `json:"manifestSHA256"`
	Material       *Material `json:"material"`
}

type Material struct {
	Provider      string            `json:"provider"`
	CaseID        string            `json:"caseId"`
	StoryID       string            `json:"storyId"`
	Attempt       string            `json:"attempt"`
	Path          string            `json:"path"`
	Files         tool.Object       `json:"files"`
	Contracts     map[string]string `json:"contracts"`
	OfficialSDK   tool.Object       `json:"officialSDK"`
	Qualification tool.Object       `json:"qualification"`
	Plan          json.RawMessage   `json:"plan"`
}

func Read() (*Manifest, string, error) {
	raw, err := fs.ReadFile(data.Builtin(), "builtin/manifest.json")
	if err != nil {
		return nil, "", err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", err
	}
	if m.SchemaVersion != 1 || len(m.Materials) == 0 {
		return nil, "", errors.New("invalid or empty builtin manifest; regenerate")
	}
	catalog := data.Scenarios()
	for name, hash := range m.Catalog {
		b, e := fs.ReadFile(catalog, name)
		if e != nil {
			return nil, "", e
		}
		if tool.DigestBytes(b) != hash {
			return nil, "", fmt.Errorf("builtin catalog drift: %s", name)
		}
	}
	return &m, tool.DigestBytes(raw), nil
}

func Select(provider, id string) (*Material, string, error) {
	m, hash, err := Read()
	if err != nil {
		return nil, "", err
	}
	for _, row := range m.Coverage {
		if row["provider"] == provider && row["caseId"] == id && row["available"] != true {
			return nil, "", fmt.Errorf("builtin unavailable %s/%s: %s (%s)", provider, id, row["missingReason"], row["verification"])
		}
	}
	for i := range m.Materials {
		material := &m.Materials[i]
		if material.Provider == provider && material.CaseID == id {
			return material, hash, nil
		}
	}
	return nil, "", fmt.Errorf("unknown builtin case %s/%s", provider, id)
}

// SelectOptional distinguishes an absent case material from an unavailable or
// corrupt builtin snapshot.
func SelectOptional(provider, id string) (*Material, string, bool, error) {
	m, hash, err := Read()
	if err != nil {
		return nil, "", false, err
	}
	foundCoverage := false
	for _, row := range m.Coverage {
		if row["provider"] == provider && row["caseId"] == id {
			foundCoverage = true
			if row["available"] != true {
				return nil, "", false, nil
			}
		}
	}
	for i := range m.Materials {
		material := &m.Materials[i]
		if material.Provider == provider && material.CaseID == id {
			return material, hash, true, nil
		}
	}
	if foundCoverage {
		return nil, "", false, fmt.Errorf("builtin coverage has no material for %s/%s", provider, id)
	}
	return nil, "", false, nil
}

func Extract(material *Material) (root string, err error) {
	m, _, err := Read()
	if err != nil {
		return "", err
	}
	archive, err := fs.ReadFile(data.Builtin(), "builtin/materials.tar.gz")
	if err != nil {
		return "", err
	}
	if tool.DigestBytes(archive) != m.ArchiveSHA256 {
		return "", errors.New("builtin archive hash drift")
	}
	root, err = os.MkdirTemp("", "agent-sdk-testify-builtin-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(root))
		}
	}()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return root, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	prefix := material.Path + "/"
	for {
		header, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return root, e
		}
		if !strings.HasPrefix(header.Name, prefix) {
			continue
		}
		name := strings.TrimPrefix(header.Name, prefix)
		expected, ok := material.Files[name]
		if !ok || !corpus.Local(name) || seen[name] || header.Typeflag != tar.TypeReg {
			return root, fmt.Errorf("invalid builtin member: %s", name)
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			return root, e
		}
		if tool.DigestBytes(b) != expected {
			return root, fmt.Errorf("builtin material hash drift: %s", name)
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return root, err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return root, err
		}
		seen[name] = true
	}
	if len(seen) != len(material.Files) {
		return root, errors.New("missing builtin material closure")
	}
	return root, nil
}

func CheckSelection(material *Material) error {
	if material == nil {
		return errors.New("missing builtin material identity")
	}
	canonical, _, err := Select(material.Provider, material.CaseID)
	if err != nil {
		return err
	}
	// Runtime descriptors cannot substitute a different hash inventory.
	a, err := json.Marshal(material)
	if err != nil {
		return fmt.Errorf("encode selected material: %w", err)
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return fmt.Errorf("encode canonical material: %w", err)
	}
	if !bytes.Equal(a, b) {
		return errors.New("builtin selection drift")
	}
	return nil
}

func CheckMaterial(root string, material *Material) error {
	return checkMaterial(root, material, false)
}

func checkMaterial(root string, material *Material, materialDescriptor bool) error {
	if err := CheckSelection(material); err != nil {
		return err
	}
	if material == nil {
		return errors.New("missing builtin material identity")
	}
	files, err := corpus.Inventory(root, false)
	if err != nil {
		return err
	}
	if materialDescriptor && material.Files["material.json"] == nil {
		delete(files, "material.json")
	}
	if len(files) != len(material.Files) {
		return errors.New("builtin inventory drift")
	}
	for name, expected := range material.Files {
		if len(tool.Str(expected)) != 64 || files[name] != expected {
			return errors.New("material hash mismatch: " + name)
		}
	}
	return nil
}

func CheckRoot(root string) (*Material, error) { return checkRoot(root, false) }

// CheckPreparedRoot accepts the fixture-owned material descriptor in addition
// to the exact builtin payload. All payload bytes still match the snapshot.
func CheckPreparedRoot(root string) (*Material, error) { return checkRoot(root, true) }

func checkRoot(root string, materialDescriptor bool) (*Material, error) {
	files, err := corpus.Inventory(root, false)
	if err != nil {
		return nil, err
	}
	m, _, err := Read()
	if err != nil {
		return nil, err
	}
	for i := range m.Materials {
		material := &m.Materials[i]
		count := len(files)
		if materialDescriptor && material.Files["material.json"] == nil && files["material.json"] != nil {
			count--
		}
		if count != len(material.Files) {
			continue
		}
		matches := true
		for name, expected := range material.Files {
			if files[name] != expected {
				matches = false
				break
			}
		}
		if matches {
			if err := checkMaterial(root, material, materialDescriptor); err != nil {
				return nil, err
			}
			return material, nil
		}
	}
	return nil, errors.New("material is not a builtin snapshot")
}
