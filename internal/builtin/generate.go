//go:build builtin_generate

package builtin

// Build-time only: no fourth installed command or runtime compiler dependency.
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
	"regexp"
	"sort"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type GenerateOptions struct {
	Root        string
	Revision    string
	Check       bool
	Diagnostics io.Writer
}

func Generate(o GenerateOptions) error {
	if o.Root == "" {
		o.Root = "."
	}
	if o.Diagnostics == nil {
		o.Diagnostics = io.Discard
	}
	target := filepath.Join(o.Root, "data", "builtin")
	var previous Manifest
	if o.Check {
		if err := tool.ReadJSON(filepath.Join(target, "manifest.json"), &previous); err != nil {
			return err
		}
		if o.Revision == "" {
			o.Revision = previous.SourceRevision
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(o.Revision) {
		return errors.New("full source --revision required")
	}
	toolVersion := version.String()
	if o.Check && !version.ExactModuleVersion(toolVersion) {
		toolVersion = previous.ToolVersion
	}
	if !version.ExactModuleVersion(toolVersion) {
		return fmt.Errorf("builtin generation requires an exact Git tag, got %q", toolVersion)
	}
	if o.Check && toolVersion != previous.ToolVersion {
		return fmt.Errorf("builtin drift: toolVersion is %s, want %s", toolVersion, previous.ToolVersion)
	}
	c := corpus.Corpus{Index: filepath.Join(o.Root, "data", "index.json"), Scenarios: filepath.Join(o.Root, "data", "scenarios")}
	index, err := c.LoadIndex()
	if err != nil {
		return err
	}
	m := Manifest{SchemaVersion: 1, ToolVersion: toolVersion, SourceRevision: o.Revision, Scope: "local technical candidates; public release not authorized", Catalog: map[string]string{}, Coverage: []tool.Object{}, Materials: []Material{}}
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
		core, _, e := corpus.RecordingRoot(source)
		if e != nil {
			return e
		}
		plan, e := os.ReadFile(filepath.Join(core, "plan.json"))
		if e != nil {
			return e
		}
		sdk, qualification := tool.Obj(row["officialSDK"]), tool.Obj(row["qualification"])
		if _, reduced := row["materialSHA256"]; !reduced {
			review, e := corpus.Read(filepath.Join(source, "review.json"))
			if e != nil {
				return e
			}
			reportPath := filepath.Join(core, "report.json")
			if selected := tool.Str(tool.Obj(review["record"])["path"]); selected != "" {
				reportPath = filepath.Join(source, filepath.Dir(selected), "report.json")
			}
			report, e := corpus.Read(reportPath)
			if e != nil {
				return e
			}
			sdk, qualification = tool.Obj(report["officialSDK"]), tool.Obj(review["qualification"])
		}
		material := Material{Provider: tool.Str(row["provider"]), CaseID: tool.Str(row["caseId"]), StoryID: tool.Str(row["storyId"]), Attempt: filepath.Base(source), Path: tool.Str(row["materials"]), Files: inventory, OfficialSDK: sdk, Qualification: qualification, Contracts: map[string]string{}, Plan: plan}
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
			if writeErr := tw.WriteHeader(&tar.Header{Name: material.Path + "/" + name, Mode: 0o600, Size: int64(len(data)), Format: tar.FormatPAX}); writeErr != nil {
				return writeErr
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
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
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
		if o.Check {
			old, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if !bytes.Equal(old, output.data) {
				return fmt.Errorf("builtin drift: %s; regenerate after qualification/adoption", output.name)
			}
		} else {
			if err := os.WriteFile(path, output.data, 0o600); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(o.Diagnostics, "builtin: %d materials / %d coverage entries, archive %d bytes (check=%t)\n", len(m.Materials), len(m.Coverage), archive.Len(), o.Check)
	return nil
}
