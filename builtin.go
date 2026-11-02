package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	protocolcmd "github.com/zoumo/agent-sdk-testify/protocol/command"
)

// Generated from the authoritative catalog/index/adoption closure, never edited.
//
//go:embed builtin/manifest.json builtin/materials.tar.gz
var builtinFiles embed.FS

type builtinManifest struct {
	SchemaVersion  int               `json:"schemaVersion"`
	ToolVersion    string            `json:"toolVersion"`
	SourceRevision string            `json:"sourceRevision"`
	Scope          string            `json:"scope"`
	IndexSHA256    string            `json:"indexSHA256"`
	Catalog        map[string]string `json:"catalog"`
	ArchiveSHA256  string            `json:"archiveSHA256"`
	Coverage       []object          `json:"coverage"`
	Materials      []builtinMaterial `json:"materials"`
}
type builtinSelection struct {
	ManifestSHA256 string           `json:"manifestSHA256"`
	Material       *builtinMaterial `json:"material"`
}

type builtinMaterial struct {
	Provider      string            `json:"provider"`
	CaseID        string            `json:"caseId"`
	StoryID       string            `json:"storyId"`
	Attempt       string            `json:"attempt"`
	Path          string            `json:"path"`
	Files         object            `json:"files"`
	Contracts     map[string]string `json:"contracts"`
	OfficialSDK   object            `json:"officialSDK"`
	Qualification object            `json:"qualification"`
	Plan          json.RawMessage   `json:"plan"`
}

func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func readBuiltin() (*builtinManifest, string, error) {
	data, err := builtinFiles.ReadFile("builtin/manifest.json")
	if err != nil {
		return nil, "", err
	}
	var m builtinManifest
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, "", err
	}
	if m.SchemaVersion != 1 || m.ToolVersion != protocolcmd.Version || len(m.Materials) == 0 {
		return nil, "", errors.New("invalid or empty builtin manifest; regenerate")
	}
	for name, hash := range m.Catalog {
		b, e := fs.ReadFile(caseCatalog, name)
		if e != nil {
			return nil, "", e
		}
		if digestBytes(b) != hash {
			return nil, "", fmt.Errorf("builtin catalog drift: %s", name)
		}
	}
	return &m, digestBytes(data), nil
}
func selectBuiltin(provider, id string) (*builtinMaterial, string, error) {
	m, hash, err := readBuiltin()
	if err != nil {
		return nil, "", err
	}
	for _, row := range m.Coverage {
		if row["provider"] == provider && row["caseId"] == id && row["available"] != true {
			return nil, "", fmt.Errorf("builtin unavailable %s/%s: %s (%s)", provider, id, row["missingReason"], row["verification"])
		}
	}
	for _, material := range m.Materials {
		if material.Provider == provider && material.CaseID == id {
			return &material, hash, nil
		}
	}
	return nil, "", fmt.Errorf("unknown builtin case %s/%s", provider, id)
}
func extractBuiltin(material *builtinMaterial) (root string, err error) {
	m, _, err := readBuiltin()
	if err != nil {
		return "", err
	}
	archive, err := builtinFiles.ReadFile("builtin/materials.tar.gz")
	if err != nil {
		return "", err
	}
	if digestBytes(archive) != m.ArchiveSHA256 {
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
		if !ok || !corpusLocal(name) || seen[name] || header.Typeflag != tar.TypeReg {
			return root, fmt.Errorf("invalid builtin member: %s", name)
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			return root, e
		}
		if digestBytes(b) != expected {
			return root, fmt.Errorf("builtin material hash drift: %s", name)
		}
		path := filepath.Join(root, name)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return root, e
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			return root, e
		}
		seen[name] = true
	}
	if len(seen) != len(material.Files) {
		return root, errors.New("missing builtin material closure")
	}
	return root, nil
}
func checkBuiltinSelection(material *builtinMaterial) error {
	if material == nil {
		return errors.New("missing builtin material identity")
	}
	canonical, _, err := selectBuiltin(material.Provider, material.CaseID)
	if err != nil {
		return err
	}
	// Runtime descriptors cannot substitute a different hash inventory.
	a, _ := json.Marshal(material)
	b, _ := json.Marshal(canonical)
	if !bytes.Equal(a, b) {
		return errors.New("builtin selection drift")
	}
	return nil
}

func checkBuiltinMaterial(root string, material *builtinMaterial) error {
	if err := checkBuiltinSelection(material); err != nil {
		return err
	}
	if material == nil {
		return errors.New("missing builtin material identity")
	}
	v := &corpusValidation{}
	v.check(len(v.inventory(root, false)) == len(material.Files), "builtin inventory drift")
	v.hashes(root, material.Files)
	return v.err
}
func mockQuery(args []string) error {
	f := flag.NewFlagSet("mock query", flag.ContinueOnError)
	list := f.Bool("list", false, "List full coverage")
	describe := f.Bool("describe", false, "Describe one case")
	provider := f.String("provider", "", "Provider filter")
	id := f.String("case", "", "Case ID")
	recording := f.String("recording", "", "Absolute external recording to describe")
	_ = f.Bool("json", false, "JSON output (schemaVersion 1)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *recording != "" {
		if !*describe || *list || *id != "" || f.NArg() != 0 {
			return errors.New("external query requires --describe --recording ABS, without --case")
		}
		return describeExternalMock(*recording, *provider)
	}
	if *list == *describe || f.NArg() != 0 || (*describe && (*provider == "" || *id == "")) || (*list && *id != "") {
		return errors.New("select --list or --describe --provider PROVIDER --case CASE")
	}
	if *provider != "" && *provider != "claude" && *provider != "codex" {
		return errors.New("unknown provider")
	}
	m, hash, err := readBuiltin()
	if err != nil {
		return err
	}
	rows := []object{}
	for _, coverage := range m.Coverage {
		if *provider != "" && coverage["provider"] != *provider {
			continue
		}
		if *id != "" && coverage["caseId"] != *id {
			continue
		}
		row := object{}
		for k, v := range coverage {
			row[k] = v
		}
		catalog, err := loadCatalogCase(caseCatalog, options{Provider: str(row["provider"]), Case: str(row["caseId"])})
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(caseCatalog, str(row["storyId"])+".json")
		if err != nil {
			return err
		}
		var story object
		if err = json.Unmarshal(data, &story); err != nil {
			return err
		}
		for _, c := range array(story["cases"]) {
			if obj(c)["id"] == row["caseId"] {
				row["contract"] = c
			}
		}
		data, err = fs.ReadFile(caseCatalog, "bindings/"+str(row["provider"])+".json")
		if err != nil {
			return err
		}
		var binding object
		if err = json.Unmarshal(data, &binding); err != nil {
			return err
		}
		row["binding"] = obj(binding["cases"])[str(row["caseId"])]
		row["scenario"] = catalog["scenario"]
		for _, material := range m.Materials {
			if material.Provider == row["provider"] && material.CaseID == row["caseId"] {
				row["material"] = material
				row["plan"] = material.Plan
				if *describe {
					inputs, e := builtinAssertionInputs(&material, hash)
					if e != nil {
						return e
					}
					row["assertionInputs"] = inputs
				}
			}
		}
		row["schemaVersion"] = 1
		row["manifestSHA256"] = hash
		row["toolVersion"] = m.ToolVersion
		rows = append(rows, row)
	}
	if *describe {
		if len(rows) != 1 {
			return errors.New("unknown case")
		}
		return writeNativeJSON(rows[0])
	}
	return writeNativeJSON(object{"schemaVersion": 1, "toolVersion": m.ToolVersion, "manifestSHA256": hash, "sourceRevision": m.SourceRevision, "scope": m.Scope, "cases": rows})
}

// Selection flags are removed before the existing direct/prepared parsers.
func mockBuiltinMode(ctx context.Context, args []string) (err error) {
	query := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		name, _, _ := strings.Cut(arg, "=")
		if name == "--list" || name == "--describe" {
			query = true
		}
	}
	if query {
		if err := mockQuery(args); err != nil {
			return protocolcmd.Fail(protocolcmd.ExitConfiguration, err)
		}
		return nil
	}
	var rest []string
	var id, provider string
	external, prepared := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, eq := strings.Cut(arg, "=")
		if name == "--recording" {
			external = true
		}
		if name == "--prepare" {
			prepared = true
		}
		if name == "--case" || name == "--provider" {
			if !eq {
				if i+1 == len(args) {
					return protocolcmd.Fail(protocolcmd.ExitConfiguration, errors.New("missing selector value"))
				}
				i++
				value = args[i]
			}
			if name == "--case" {
				if id != "" {
					return errors.New("duplicate case selector")
				}
				id = value
			} else {
				provider = value
				rest = append(rest, "--provider", value)
			}
		} else {
			rest = append(rest, arg)
		}
	}
	if id == "" {
		return mockMode(ctx, args)
	}
	if external || provider == "" {
		return protocolcmd.Fail(protocolcmd.ExitConfiguration, errors.New("--case requires --provider and excludes --recording"))
	}
	material, hash, err := selectBuiltin(provider, id)
	if err != nil {
		return protocolcmd.Fail(protocolcmd.ExitConfiguration, err)
	}
	root, err := extractBuiltin(material)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	selection := &builtinSelection{ManifestSHA256: hash, Material: material}
	if prepared {
		return mockPreparedMode(ctx, append(rest, "--recording", root), selection)
	}
	return mockExternalSelected(ctx, append([]string{"--recording", root}, rest...), selection)
}
