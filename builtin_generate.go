//go:build builtin_generate

package main

// Build-time only: no fourth installed command or runtime compiler dependency.
import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	protocolcmd "github.com/zoumo/agent-sdk-testify/protocol/command"
)

func init() {
	f := flag.NewFlagSet("builtin generation", flag.ExitOnError)
	check := f.Bool("check", false, "Reject stale or missing generated artifacts")
	revision := f.String("revision", "", "Source repository revision (required when generating)")
	root := f.String("root", ".", "Authoritative repository root")
	_ = f.Parse(os.Args[1:])
	if f.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected generation arguments")
		os.Exit(2)
	}
	if err := generateBuiltin(*root, *revision, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func generateBuiltin(root, revision string, check bool) error {
	target := filepath.Join(root, "builtin")
	if check && revision == "" {
		var old builtinManifest
		if err := readJSON(filepath.Join(target, "manifest.json"), &old); err != nil {
			return err
		}
		revision = old.SourceRevision
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) {
		return errors.New("full source --revision required")
	}
	c := corpus{Index: filepath.Join(root, "tests/simulation/recordings/index.json"), Scenarios: filepath.Join(root, "tests/simulation/scenarios")}
	index, err := c.loadIndex()
	if err != nil {
		return err
	}
	m := builtinManifest{SchemaVersion: 1, ToolVersion: protocolcmd.Version, SourceRevision: revision, Scope: "local technical candidates; public release not authorized", Catalog: map[string]string{}, Coverage: []object{}, Materials: []builtinMaterial{}}
	baseline, err := corpusRead(filepath.Join(root, "docs/import-baseline.json"))
	if err != nil {
		return err
	}
	m.ImportSource = obj(baseline["source"])
	m.IndexSHA256, err = fileDigest(c.Index)
	if err != nil {
		return err
	}
	err = fs.WalkDir(os.DirFS(c.Scenarios), ".", func(name string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(name) != ".json" {
			return nil
		}
		p, e := corpusRegular(c.Scenarios, name)
		if e != nil {
			return e
		}
		hash, e := fileDigest(p)
		m.Catalog[name] = hash
		return e
	})
	if err != nil {
		return err
	}
	// Sorted index identities and tar members, fixed headers and gzip metadata.
	rows := array(index["cases"])
	sort.Slice(rows, func(i, j int) bool {
		a, b := obj(rows[i]), obj(rows[j])
		return str(a["provider"])+str(a["caseId"]) < str(b["provider"])+str(b["caseId"])
	})
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, value := range rows {
		row := obj(value)
		available := row["adoption"] == "admitted" && row["verification"] == "passed"
		row["available"] = available
		if !available {
			row["missingReason"] = row["verificationReason"]
		}
		m.Coverage = append(m.Coverage, row)
		if !available {
			continue
		}
		source, e := c.verifyAdopted(row)
		if e != nil {
			return fmt.Errorf("%s/%s: %w", row["provider"], row["caseId"], e)
		}
		inventory, e := corpusInventory(source, false)
		if e != nil {
			return e
		}
		plan, e := os.ReadFile(filepath.Join(source, "plan.json"))
		if e != nil {
			return e
		}
		report, e := corpusRead(filepath.Join(source, "report.json"))
		if e != nil {
			return e
		}
		review, e := corpusRead(filepath.Join(source, "review.json"))
		if e != nil {
			return e
		}
		material := builtinMaterial{Provider: str(row["provider"]), CaseID: str(row["caseId"]), StoryID: str(row["storyId"]), Attempt: filepath.Base(source), Path: str(row["materials"]), Files: inventory, OfficialSDK: obj(report["officialSDK"]), Qualification: obj(review["qualification"]), Contracts: map[string]string{}, Plan: plan}
		for _, name := range []string{material.StoryID + ".json", "bindings/" + material.Provider + ".json"} {
			material.Contracts[name] = m.Catalog[name]
		}
		for _, name := range corpusKeys(inventory) {
			data, e := os.ReadFile(filepath.Join(source, name))
			if e != nil {
				return e
			}
			if digestBytes(data) != inventory[name] {
				return fmt.Errorf("material changed while generating: %s", name)
			}
			if e = tw.WriteHeader(&tar.Header{Name: material.Path + "/" + name, Mode: 0600, Size: int64(len(data)), Format: tar.FormatPAX}); e != nil {
				return e
			}
			if _, e = tw.Write(data); e != nil {
				return e
			}
		}
		m.Materials = append(m.Materials, material)
	}
	if len(m.Materials) == 0 {
		return errors.New("empty qualified builtin set")
	}
	if err = errors.Join(tw.Close(), gz.Close()); err != nil {
		return err
	}
	m.ArchiveSHA256 = digestBytes(archive.Bytes())
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, 10)
	for _, output := range []struct {
		name string
		data []byte
	}{{"manifest.json", data}, {"materials.tar.gz", archive.Bytes()}} {
		path := filepath.Join(target, output.name)
		if check {
			old, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if !bytes.Equal(old, output.data) {
				return fmt.Errorf("builtin drift: %s; regenerate after qualification/adoption", output.name)
			}
		} else {
			if err = os.WriteFile(path, output.data, 0600); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(os.Stderr, "builtin: %d materials / %d coverage entries, archive %d bytes (check=%t)\n", len(m.Materials), len(m.Coverage), archive.Len(), check)
	return nil
}
