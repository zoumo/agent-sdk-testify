package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Binding maps one native target to immutable bytes. Source is material-relative;
// nativeTarget is a logical location in the writable native environment.
type Binding struct {
	Role            string `json:"role"`
	SessionID       string `json:"sessionId"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	ChildSessionID  string `json:"childSessionId,omitempty"`
	Project         string `json:"project"`
	NativeTarget    string `json:"nativeTarget"`
	Source          string `json:"source"`
	SHA256          string `json:"sha256"`
}

func nativeTarget(name string) bool {
	return canonicalPath(name) && (strings.HasPrefix(name, "config/projects/") || strings.HasPrefix(name, "config/sessions/"))
}

func canonicalPath(name string) bool {
	return name != "" && name == path.Clean(name) && !strings.ContainsAny(name, "\\\x00") && filepath.IsLocal(filepath.FromSlash(name))
}

// CanonicalBindings validates explicit targets independently of the complete
// closure inventory. Project snapshots and source reports need no native target.
func CanonicalBindings(root string, plan, manifest tool.Object) ([]Binding, error) {
	raw, ok := plan["historyBindings"]
	if !ok {
		return nil, errors.New("material plan omits historyBindings")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var bindings []Binding
	d := json.NewDecoder(bytes.NewReader(encoded))
	d.DisallowUnknownFields()
	if err := d.Decode(&bindings); err != nil {
		return nil, err
	}
	if bindings == nil {
		return nil, errors.New("historyBindings must be an array")
	}
	seen := map[string]bool{}
	files := tool.Obj(manifest["files"])
	for _, b := range bindings {
		if b.Role != "seed" && b.Role != "runtime" {
			return nil, errors.New("unsupported history binding role")
		}
		if !nativeTarget(b.NativeTarget) || !canonicalPath(b.Source) || !validDigest(b.SHA256) {
			return nil, errors.New("invalid history binding reference or hash")
		}
		if b.SessionID == "" || b.Project == "" {
			return nil, errors.New("history binding omits native identity")
		}
		if b.ChildSessionID != "" && (b.ChildSessionID != b.SessionID || b.ParentSessionID == "" || b.ParentSessionID == b.SessionID) {
			return nil, errors.New("conflicting child history identity")
		}
		if seen[b.NativeTarget] {
			return nil, errors.New("duplicate history native target")
		}
		seen[b.NativeTarget] = true
		prefix := "history/" + b.Role + "/"
		// Seed can be a legacy full tree or a canonical seed closure. The target
		// always preserves its native relative filename, never a digest filename.
		if !strings.HasPrefix(b.Source, prefix) || !strings.HasSuffix(b.Source, "/"+b.NativeTarget) {
			return nil, errors.New("history source differs from native target")
		}
		if files[b.Source] != b.SHA256 {
			return nil, errors.New("history binding differs from manifest inventory")
		}
		if err := checkLocalFile(root, b.Source, b.SHA256); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(b.Source)))
		if err != nil {
			return nil, err
		}
		actual, err := Identify(b.NativeTarget, data, b.Project)
		if err != nil {
			return nil, err
		}
		if actual.SessionID != b.SessionID || actual.ParentSessionID != b.ParentSessionID || actual.ChildSessionID != b.ChildSessionID || actual.Project != b.Project {
			return nil, errors.New("history binding differs from native file identity")
		}
	}
	for name := range files {
		if strings.HasPrefix(name, "history/runtime/") && !seen[strings.TrimPrefix(name, "history/runtime/")] {
			return nil, fmt.Errorf("unbound runtime history: %s", name)
		}
	}
	return bindings, nil
}

// Identify extracts only native identity; raw history is never re-encoded.
func Identify(target string, data []byte, project string) (Binding, error) {
	b := Binding{NativeTarget: target, Project: project}
	if !nativeTarget(target) {
		return b, errors.New("invalid native history target")
	}
	if strings.HasPrefix(target, "config/sessions/") {
		line, _, _ := bytes.Cut(data, []byte{'\n'})
		var row tool.Object
		if err := json.Unmarshal(line, &row); err != nil {
			return b, err
		}
		payload := tool.Obj(row["payload"])
		if row["type"] != "session_meta" {
			return b, errors.New("codex history lacks session metadata")
		}
		b.SessionID = tool.Str(payload["id"])
		if !strings.HasSuffix(path.Base(target), b.SessionID+".jsonl") {
			return b, errors.New("codex native filename differs from session identity")
		}
		if cwd := tool.Str(payload["cwd"]); cwd != "" {
			b.Project = cwd
		}
		b.ParentSessionID = tool.Str(payload["parentThreadId"])
		if b.ParentSessionID == "" {
			b.ParentSessionID = tool.Str(payload["parent_thread_id"])
		}
		if b.ParentSessionID == "" {
			b.ParentSessionID = tool.Str(tool.Obj(tool.Obj(tool.Obj(payload["source"])["subagent"])["thread_spawn"])["parent_thread_id"])
		}
		if b.ParentSessionID != "" {
			b.ChildSessionID = b.SessionID
		}
	} else {
		recordedProject := ""
		base := path.Base(target)
		if strings.Contains(target, "/subagents/") {
			parts := strings.Split(target, "/")
			for i, p := range parts {
				if p == "subagents" && i > 0 {
					b.ParentSessionID = parts[i-1]
				}
			}
			b.SessionID = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".meta.json"), ".jsonl")
			b.ChildSessionID = b.SessionID
		} else {
			b.SessionID = strings.TrimSuffix(strings.TrimSuffix(base, ".jsonl"), ".json")
		}
		for line := range bytes.SplitSeq(data, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var row tool.Object
			if err := json.Unmarshal(line, &row); err != nil {
				return b, err
			}
			if cwd := tool.Str(row["cwd"]); cwd != "" {
				if recordedProject != "" && recordedProject != cwd {
					return b, errors.New("claude history project identity differs")
				}
				recordedProject = cwd
			}
			expectedSession := b.SessionID
			if b.ParentSessionID != "" {
				expectedSession = b.ParentSessionID
			}
			if session := tool.Str(row["sessionId"]); session != "" && session != expectedSession {
				return b, errors.New("claude native filename differs from session identity")
			}
			if agent := tool.Str(row["agentId"]); agent != "" && b.ChildSessionID != "" && agent != b.ChildSessionID {
				return b, errors.New("claude native filename differs from child identity")
			}
		}
		if recordedProject != "" {
			b.Project = recordedProject
		}
		key := regexp.MustCompile(`[^a-zA-Z0-9]`)
		if !strings.HasPrefix(target, "config/projects/"+key.ReplaceAllString(b.Project, "-")+"/") {
			return b, errors.New("claude native target differs from project identity")
		}
	}
	if b.SessionID == "" || b.Project == "" {
		return b, errors.New("native history identity is incomplete")
	}
	return b, nil
}

// CaptureCanonical closes a full frozen seed and only changed native runtime
// files. Reuse is by the same declared target and identity, never a hash search.
func CaptureCanonical(seed, overlay, core string, plan tool.Object, startupDerived map[string]string) ([]Binding, error) {
	bindings := map[string]Binding{}
	seedRoot := filepath.Join(core, "history", "seed")
	if seed != "" {
		before, err := HashTree(seed)
		if err != nil {
			return nil, err
		}
		if err := tool.CopyTree(seed, seedRoot); err != nil {
			return nil, err
		}
		after, err := HashTree(seedRoot)
		if err != nil {
			return nil, err
		}
		if len(before) != len(after) {
			return nil, errors.New("seed closure changed while copying")
		}
		for name, hash := range before {
			if after[name] != hash {
				return nil, errors.New("seed closure hash changed")
			}
		}
		seedSource := SeedSource(seedRoot)
		sourcePrefix, _ := filepath.Rel(seedRoot, seedSource)
		for name, hash := range before {
			target := name
			if sourcePrefix != "." {
				target = strings.TrimPrefix(name, filepath.ToSlash(sourcePrefix)+"/")
			}
			if !nativeTarget(target) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(seedRoot, filepath.FromSlash(name)))
			if err != nil {
				return nil, err
			}
			b, err := identifyCaptured(seedRoot, name, target, data, historyProject(plan))
			if err != nil {
				return nil, err
			}
			b.Role = "seed"
			b.Source = "history/seed/" + name
			b.SHA256 = hash
			bindings[target] = b
		}
		// A format 3 seed uses its explicit native bindings inside its closure.
		var sm, sp tool.Object
		if tool.ReadJSON(filepath.Join(seedSource, "manifest.json"), &sm) == nil && sm["materialFormatVersion"] == float64(3) {
			if err := tool.ReadJSON(filepath.Join(seedSource, "plan.json"), &sp); err != nil {
				return nil, err
			}
			sb, err := CanonicalBindings(seedSource, sp, sm)
			if err != nil {
				return nil, err
			}
			for _, b := range sb {
				if sourcePrefix == "." {
					b.Source = "history/seed/" + b.Source
				} else {
					b.Source = "history/seed/" + filepath.ToSlash(sourcePrefix) + "/" + b.Source
				}
				b.Role = "seed"
				bindings[b.NativeTarget] = b
			}
		}
	}
	for _, prefix := range []string{"config/projects", "config/sessions"} {
		base := filepath.Join(overlay, filepath.FromSlash(prefix))
		if _, err := os.Lstat(base); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		hashes, err := HashTree(base)
		if err != nil {
			return nil, err
		}
		for name, hash := range hashes {
			target := path.Join(prefix, name)
			if startupDerived[target] == hash {
				continue
			}
			data, err := os.ReadFile(filepath.Join(overlay, filepath.FromSlash(target)))
			if err != nil {
				return nil, err
			}
			b, err := identifyCaptured(overlay, target, target, data, historyProject(plan))
			if err != nil {
				return nil, err
			}
			if old, ok := bindings[target]; ok && old.SHA256 == hash && old.SessionID == b.SessionID && old.Project == b.Project {
				continue
			}
			b.Role = "runtime"
			b.Source = "history/runtime/" + target
			b.SHA256 = hash
			if err := copyVerified(filepath.Join(overlay, filepath.FromSlash(target)), filepath.Join(core, filepath.FromSlash(b.Source)), hash); err != nil {
				return nil, err
			}
			bindings[target] = b
		}
	}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Binding, 0, len(names))
	for _, name := range names {
		result = append(result, bindings[name])
	}
	return result, nil
}

func identifyCaptured(root, name, target string, data []byte, project string) (Binding, error) {
	if stem, ok := strings.CutSuffix(name, ".meta.json"); ok {
		sibling := stem + ".jsonl"
		if siblingData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sibling))); err == nil {
			identity, err := Identify(strings.TrimSuffix(target, ".meta.json")+".jsonl", siblingData, project)
			if err != nil {
				return Binding{}, err
			}
			project = identity.Project
		} else if !os.IsNotExist(err) {
			return Binding{}, err
		}
	}
	return Identify(target, data, project)
}

// StartupDerived identifies the sealed startup copies. Record may subsequently
// change these native files; only unchanged copies can be omitted from runtime.
func StartupDerived(execution string) (map[string]string, error) {
	var receipt tool.Object
	if err := tool.ReadJSON(filepath.Join(execution, "materialization.json"), &receipt); err != nil {
		return nil, err
	}
	if receipt["version"] != float64(2) {
		return nil, errors.New("unsupported history materialization version")
	}
	out := map[string]string{}
	for _, value := range tool.Array(receipt["transformations"]) {
		tr := tool.Obj(value)
		target, hash := tool.Str(tr["nativeTarget"]), tool.Str(tr["derivedSHA256"])
		if !nativeTarget(target) || !validDigest(hash) || out[target] != "" {
			return nil, errors.New("invalid startup history binding")
		}
		out[target] = hash
	}
	return out, nil
}

func historyProject(plan tool.Object) string {
	for _, name := range []string{"historyProject", "_fixtureProject", "project"} {
		if p := tool.Str(plan[name]); p != "" {
			return p
		}
	}
	return ""
}

func materializeCanonical(source, target string, plan, manifest tool.Object) (map[string]string, error) {
	bindings, err := CanonicalBindings(source, plan, manifest)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	transformations := make([]tool.Object, 0)
	for _, b := range bindings {
		destination := filepath.Join(target, filepath.FromSlash(b.NativeTarget))
		if err := copyVerified(filepath.Join(source, filepath.FromSlash(b.Source)), destination, b.SHA256); err != nil {
			return nil, err
		}
		result[b.NativeTarget] = b.SHA256
	}
	if err := Check(target, result); err != nil {
		return nil, err
	}
	// Canonical Claude derivations are implemented separately; the sealed bytes
	// belong to the execution, while native targets remain disposable copies.
	if plan["provider"] == "claude" || plan["agentType"] == "claude" {
		for _, b := range bindings {
			tr, err := deriveCanonicalClaude(source, target, filepath.Join(target, "workspace"), b)
			if err != nil {
				return nil, err
			}
			if tr != nil {
				transformations = append(transformations, tr)
				result[tool.Str(tr["nativeTarget"])] = tool.Str(tr["derivedSHA256"])
			}
		}
	}
	if err := tool.WriteJSON(filepath.Join(target, "materialization.json"), tool.Object{"version": 2, "transformations": transformations}); err != nil {
		return nil, err
	}
	return result, nil
}

// MaterializeSeed prepares only native startup files; it never clones a frozen
// source package into a baseline, template, route or writable execution.
func MaterializeSeed(source, target string, plan tool.Object) (map[string]string, error) {
	source = SeedSource(source)
	var manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(source, "manifest.json"), &manifest); err != nil {
		return nil, err
	}
	if manifest["materialFormatVersion"] == float64(3) {
		var p tool.Object
		if err := tool.ReadJSON(filepath.Join(source, "plan.json"), &p); err != nil {
			return nil, err
		}
		bindings, err := CanonicalBindings(source, p, manifest)
		if err != nil {
			return nil, err
		}
		result := map[string]string{}
		for _, b := range bindings {
			if err := copyVerified(filepath.Join(source, filepath.FromSlash(b.Source)), filepath.Join(target, filepath.FromSlash(b.NativeTarget)), b.SHA256); err != nil {
				return nil, err
			}
			result[b.NativeTarget] = b.SHA256
		}
		return result, Check(target, result)
	}
	files, err := Bindings(source)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for name, hash := range files {
		if !nativeTarget(name) {
			continue
		}
		if err := copyVerified(filepath.Join(source, filepath.FromSlash(name)), filepath.Join(target, filepath.FromSlash(name)), hash); err != nil {
			return nil, err
		}
		result[name] = hash
	}
	_ = plan
	return result, Check(target, result)
}

// RebindMaterialization runs before the execution envelope is sealed. Both the
// source bytes and sealed derived bytes must exist at their final references.
func RebindMaterialization(packageRoot, execution, oldPrefix, newPrefix string) error {
	var receipt tool.Object
	if err := tool.ReadJSON(filepath.Join(execution, "materialization.json"), &receipt); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if receipt["version"] != float64(2) {
		return errors.New("unsupported history materialization version")
	}
	for _, value := range tool.Array(receipt["transformations"]) {
		tr := tool.Obj(value)
		source := tool.Str(tr["source"])
		if strings.HasPrefix(source, newPrefix+"/") {
			// A pre-publication retry may already have completed the rebind.
		} else if strings.HasPrefix(source, oldPrefix+"/") {
			source = newPrefix + strings.TrimPrefix(source, oldPrefix)
		} else {
			return errors.New("materialization source differs from capture")
		}
		if err := checkLocalFile(packageRoot, source, tool.Str(tr["sourceSHA256"])); err != nil {
			return err
		}
		if err := checkLocalFile(packageRoot, tool.Str(tr["derived"]), tool.Str(tr["derivedSHA256"])); err != nil {
			return err
		}
		tr["source"] = source
	}
	return tool.WriteJSON(filepath.Join(execution, "materialization.json"), receipt)
}

// SealMaterialization moves the only persistent derived bytes into the execution
// and replaces runtime-tree references with package-relative references.
func SealMaterialization(overlay, execution, sourceReference string) error {
	var receipt tool.Object
	if err := tool.ReadJSON(filepath.Join(overlay, "materialization.json"), &receipt); os.IsNotExist(err) {
		return tool.WriteJSON(filepath.Join(execution, "materialization.json"), tool.Object{"version": 2, "transformations": []any{}})
	} else if err != nil {
		return err
	}
	if receipt["version"] == float64(1) {
		for _, value := range tool.Array(receipt["transformations"]) {
			tr := tool.Obj(value)
			target := tool.Str(tr["derived"])
			if !nativeTarget(target) {
				return errors.New("invalid legacy native derivation target")
			}
			derived := "history/derived/" + target
			if err := copyVerified(filepath.Join(overlay, filepath.FromSlash(target)), filepath.Join(overlay, filepath.FromSlash(derived)), tool.Str(tr["derivedSHA256"])); err != nil {
				return err
			}
			tr["nativeTarget"] = target
			tr["derived"] = derived
		}
		receipt["version"] = 2
	} else if receipt["version"] != float64(2) {
		return errors.New("unsupported history materialization version")
	}
	if receipt["transformations"] == nil {
		receipt["transformations"] = []any{}
	}
	for _, value := range tool.Array(receipt["transformations"]) {
		tr := tool.Obj(value)
		source := tool.Str(tr["source"])
		derived := tool.Str(tr["derived"])
		if !canonicalPath(source) || !canonicalPath(derived) || !strings.HasPrefix(derived, "history/derived/") {
			return errors.New("invalid materialization reference")
		}
		tr["source"] = path.Join(sourceReference, source)
		target := filepath.Join(execution, filepath.FromSlash(derived))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(overlay, filepath.FromSlash(derived)), target); err != nil {
			return err
		}
		tr["derived"] = path.Join("executions", filepath.Base(execution), derived)
		if err := checkLocalFile(execution, derived, tool.Str(tr["derivedSHA256"])); err != nil {
			return err
		}
	}
	if err := tool.WriteJSON(filepath.Join(execution, "materialization.json"), receipt); err != nil {
		return err
	}
	return os.Remove(filepath.Join(overlay, "materialization.json"))
}

// SeedSource recognizes the fixed complete source/project closure. Legacy seed
// roots remain readable at their original location.
func SeedSource(root string) string {
	// These are the two supported package positions inside the frozen seed
	// closure. Package validation remains the corpus reader's responsibility.
	for _, name := range []string{"material", "source/material"} {
		if info, err := os.Lstat(filepath.Join(root, name, "material.json")); err == nil && info.Mode().IsRegular() {
			return filepath.Join(root, name)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "plan.json")); os.IsNotExist(err) {
		if info, e := os.Lstat(filepath.Join(root, "source", "plan.json")); e == nil && info.Mode().IsRegular() {
			return filepath.Join(root, "source")
		}
	}
	return root
}
