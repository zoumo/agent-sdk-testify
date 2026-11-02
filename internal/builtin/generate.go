//go:build builtin_generate

package builtin

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

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
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
	target := filepath.Join(root, "data", "builtin")
	if check && revision == "" {
		var old Manifest
		if err := tool.ReadJSON(filepath.Join(target, "manifest.json"), &old); err != nil {
			return err
		}
		revision = old.SourceRevision
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) {
		return errors.New("full source --revision required")
	}
	c := corpus.Corpus{Index: filepath.Join(root, "data/index.json"), Scenarios: filepath.Join(root, "data/scenarios")}
	index, err := c.LoadIndex()
	if err != nil {
		return err
	}
	m := Manifest{SchemaVersion: 1, ToolVersion: protocolcmd.Version, SourceRevision: revision, Scope: "local technical candidates; public release not authorized", Catalog: map[string]string{}, Coverage: []tool.Object{}, Materials: []Material{}}
	m.IndexSHA256, err = tool.FileDigest(c.Index)
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
		p, e := corpus.Regular(c.Scenarios, name)
		if e != nil {
			return e
		}
		hash, e := tool.FileDigest(p)
		m.Catalog[name] = hash
		return e
	})
	if err != nil {
		return err
	}
	// Sorted index identities and tar members, fixed headers and gzip metadata.
	rows := tool.Array(index["cases"])
	sort.Slice(rows, func(i, j int) bool {
		a, b := tool.Obj(rows[i]), tool.Obj(rows[j])
		return tool.Str(a["provider"])+tool.Str(a["caseId"]) < tool.Str(b["provider"])+tool.Str(b["caseId"])
	})
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, value := range rows {
		row := tool.Obj(value)
		available := row["adoption"] == "admitted" && row["verification"] == "passed"
		row["available"] = available
		if !available {
			row["missingReason"] = row["verificationReason"]
		}
		m.Coverage = append(m.Coverage, row)
		if !available {
			continue
		}
		source, e := c.VerifyAdopted(row)
		if e != nil {
			return fmt.Errorf("%s/%s: %w", row["provider"], row["caseId"], e)
		}
		inventory, e := corpus.Inventory(source, false)
		if e != nil {
			return e
		}
		plan, e := os.ReadFile(filepath.Join(source, "plan.json"))
		if e != nil {
			return e
		}
		report, e := corpus.Read(filepath.Join(source, "report.json"))
		if e != nil {
			return e
		}
		review, e := corpus.Read(filepath.Join(source, "review.json"))
		if e != nil {
			return e
		}
		material := Material{Provider: tool.Str(row["provider"]), CaseID: tool.Str(row["caseId"]), StoryID: tool.Str(row["storyId"]), Attempt: filepath.Base(source), Path: tool.Str(row["materials"]), Files: inventory, OfficialSDK: tool.Obj(report["officialSDK"]), Qualification: tool.Obj(review["qualification"]), Contracts: map[string]string{}, Plan: plan}
		for _, name := range []string{material.StoryID + ".json", "bindings/" + material.Provider + ".json"} {
			material.Contracts[name] = m.Catalog[name]
		}
		for _, name := range corpus.Keys(inventory) {
			data, e := os.ReadFile(filepath.Join(source, name))
			if e != nil {
				return e
			}
			if tool.DigestBytes(data) != inventory[name] {
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
	m.ArchiveSHA256 = tool.DigestBytes(archive.Bytes())
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
