package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/rawjson"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Corpus policy belongs to the Go tool, not to an SDK driver. Paths are explicit
// so the compiled binary can operate outside its source checkout.
type Corpus struct {
	Index, Scenarios, Archive string
}

var SourceEvidence = []string{"plan.json", "manifest.json", "report.json", "result.json", "summary.json"}

func claudeProjectKey(cwd string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-")
}

func Local(name string) bool {
	return name != "" && name != "." && !strings.Contains(name, "\\") && path.Clean(name) == name && filepath.IsLocal(name)
}

func Regular(root, name string) (string, error) {
	if !Local(name) {
		return "", fmt.Errorf("non-local or non-canonical material path: %q", name)
	}
	p := root
	for part := range strings.SplitSeq(name, "/") {
		p = filepath.Join(p, part)
		i, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("material symlink is forbidden: %s", name)
		}
	}
	i, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !i.Mode().IsRegular() {
		return "", fmt.Errorf("required regular material missing: %s", name)
	}
	return p, nil
}

func Digest(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func Inventory(root string, empty bool) (tool.Object, error) {
	i, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("material directory is missing or linked")
	}
	out := tool.Object{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("material symlink is forbidden")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("non-regular material is forbidden")
		}
		name, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		h, err := Digest(p)
		if err == nil {
			out[filepath.ToSlash(name)] = h
		}
		return err
	})
	if err == nil && !empty && len(out) == 0 {
		err = errors.New("empty material inventory")
	}
	return out, err
}

func Read(p string) (tool.Object, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if _, err = rawjson.Parse(b); err != nil {
		return nil, err
	}
	var v tool.Object
	err = json.Unmarshal(b, &v)
	if err == nil && v == nil {
		err = errors.New("JSON evidence must be an object")
	}
	return v, err
}

func Write(p string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Validation keeps the first error. Subsequent reads/checks cannot turn a
// missing or malformed artifact into a successful qualification.
type Validation struct{ Err error }

func (v *Validation) Check(ok bool, message string) {
	if v.Err == nil && !ok {
		v.Err = errors.New(message)
	}
}

func (v *Validation) Read(root, name string) tool.Object {
	if v.Err != nil {
		return tool.Object{}
	}
	p, err := Regular(root, name)
	if err != nil {
		v.Err = err
		return tool.Object{}
	}
	m, err := Read(p)
	v.Err = err
	return m
}

func (v *Validation) Digest(root, name string) string {
	if v.Err != nil {
		return ""
	}
	p, err := Regular(root, name)
	if err != nil {
		v.Err = err
		return ""
	}
	h, err := Digest(p)
	v.Err = err
	return h
}

func (v *Validation) Hashes(root string, hashes tool.Object) {
	v.Check(len(hashes) > 0, "nonempty hash inventory required")
	for name, expected := range hashes {
		v.Check(len(tool.Str(expected)) == 64 && v.Digest(root, name) == expected, "material hash mismatch: "+name)
	}
}

func (v *Validation) Inventory(root string, empty bool) tool.Object {
	if v.Err != nil {
		return tool.Object{}
	}
	x, err := Inventory(root, empty)
	v.Err = err
	return x
}

func Keys(m tool.Object) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func corpusSet(names []string) tool.Object {
	m := tool.Object{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func Within(p, root string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && (r == "." || filepath.IsLocal(r))
}

func (c *Corpus) Catalog() (tool.Object, error) {
	v := &Validation{}
	index := v.Read(c.Scenarios, "index.json")
	out := tool.Object{}
	for _, story := range tool.Strings(index["stories"]) {
		s := v.Read(c.Scenarios, story+".json")
		for _, item := range tool.Array(s["cases"]) {
			definition := tool.Obj(item)
			id := tool.Str(definition["id"])
			for _, provider := range []string{"codex", "claude"} {
				binding := v.Read(c.Scenarios, "bindings/"+provider+".json")
				mode := tool.Str(tool.Obj(definition["execution"])["mode"])
				if x := tool.Str(tool.Obj(tool.Obj(tool.Obj(binding["cases"])[id])["execution"])["mode"]); x != "" {
					mode = x
				}
				required, ok := definition["required"].(bool)
				v.Check(ok, "case requirement must be explicit")
				if override, exists := tool.Obj(tool.Obj(binding["cases"])[id])["required"]; exists {
					required, ok = override.(bool)
					v.Check(ok, "provider requirement must be boolean")
				}
				requirement := "optional"
				if required {
					requirement = "required"
				}
				out[provider+"/"+id] = tool.Object{"story": story, "requirement": requirement, "route": mode, "driverProfile": definition["driverProfile"]}
			}
		}
	}
	return out, v.Err
}

func (c *Corpus) LoadIndex() (tool.Object, error) {
	index, err := Read(c.Index)
	if err != nil {
		return nil, err
	}
	expected, err := c.Catalog()
	if err != nil {
		return nil, err
	}
	v := &Validation{}
	v.Check(index["version"] == float64(1), "unsupported corpus index version")
	seen := map[string]bool{}
	for _, item := range tool.Array(index["cases"]) {
		e := tool.Obj(item)
		key := tool.Str(e["provider"]) + "/" + tool.Str(e["caseId"])
		target, ok := expected[key]
		v.Check(ok && !seen[key], "unknown or duplicate corpus identity")
		seen[key] = true
		t := tool.Obj(target)
		v.Check(e["storyId"] == t["story"] && e["requirement"] == t["requirement"], "corpus requirement differs from catalog")
		state := tool.Str(e["adoption"])
		v.Check(state == "" || state == "missing" || state == "untriggered" || state == "admitted", "invalid adoption state")
		verification := tool.Str(e["verification"])
		v.Check(corpusSet([]string{"passed", "failed", "unverified", "blocked", "unsupported"})[verification] == true, "invalid verification state")
		v.Check(verification == "passed" || strings.TrimSpace(tool.Str(e["verificationReason"])) != "", "non-passing verification needs a reason")
		v.Check(verification != "passed" || state == "admitted", "passing verification requires admitted evidence")
	}
	v.Check(len(seen) > 0 && len(seen) == len(expected), "corpus index omits required identities")
	return index, v.Err
}

func corpusPublishable(name string) bool {
	if !Local(name) {
		return false
	}
	allowed := corpusSet(append(append([]string{}, SourceEvidence...), "remote-service.json", "source-plan.json", "official-origin.json"))[name] == true
	for _, prefix := range []string{"raw/", "history-seed/", "driver/", "config/projects/", "config/sessions/", "remote-probe/raw/", "origin/"} {
		allowed = allowed || strings.HasPrefix(name, prefix)
	}
	for _, forbidden := range []string{"credentials", "auth.json", ".private", ".env"} {
		if strings.Contains(strings.ToLower(name), forbidden) {
			return false
		}
	}
	return allowed
}

func Phases(plan tool.Object) [][2]string {
	out := [][2]string{{"", ""}}
	if plan["driverProfile"] == "remote-close" {
		out = append(out, [2]string{"remote-probe/", "remote-probe/"})
	}
	return out
}

func (v *Validation) Summary(root, mode string, plan tool.Object) tool.Object {
	s := v.Read(root, "summary.json")
	v.Check(s["mode"] == mode && s["provider"] == plan["provider"] && s["case"] == plan["caseId"], "run identity or mode differs")
	v.Check(s["error"] == nil && tool.Obj(s["result"])["status"] == "passed", "run did not pass")
	v.Check(v.Read(root, "result.json")["status"] == "passed", "business result did not pass")
	return s
}

func (v *Validation) Replay(source, replay string, plan tool.Object, mode string) {
	if v.publicVerification(source, replay, plan) {
		return
	}
	actualMode := mode
	if mode == "replay" {
		var summary tool.Object
		if tool.ReadJSON(filepath.Join(replay, "summary.json"), &summary) == nil && summary["mode"] == "verify" {
			actualMode = "verify"
			attestation := v.Read(replay, "verification.json")
			v.Check(attestation["schemaVersion"] == float64(1) && attestation["status"] == "passed" && attestation["error"] == nil, "official verification did not pass")
			v.Hashes(replay, tool.Obj(attestation["files"]))
			for _, name := range []string{"summary.json", "result.json", "replay-report.json"} {
				v.Check(tool.Obj(attestation["files"])[name] != nil, "verification omits evidence hash")
			}
			for _, phase := range Phases(plan) {
				name := "main"
				if phase[0] != "" {
					name = "probe"
				}
				lifecycle := "peer-" + name + "-lifecycle.json"
				v.Check(tool.Obj(attestation["files"])[phase[0]+"replay-peer.json"] != nil && tool.Obj(attestation["files"])[lifecycle] != nil, "verification omits selected connection")
			}
		}
	}
	s := v.Summary(replay, actualMode, plan)
	original := tool.Object{}
	for _, name := range SourceEvidence {
		original[name] = v.Digest(source, name)
	}
	v.Check(reflect.DeepEqual(s["source"], original), "run does not bind original source plan and manifest")
	for _, phase := range Phases(plan) {
		r := v.Read(replay, phase[0]+"replay-peer.json")
		raw := phase[1] + "raw/raw.jsonl"
		v.Check(r["provider"] == plan["provider"] && r["recordingSHA256"] == v.Digest(source, raw) && r["fullConsumption"] == true && r["clientEOF"] == true && r["nativeLaunched"] == false && r["error"] == nil, "incomplete or incorrect mock receipt")
		if v.Err == nil {
			b, err := os.ReadFile(filepath.Join(source, raw))
			v.Err = err
			v.Check(r["frames"] == float64(len(bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")))), "receipt frame count differs")
		}
	}
	v.Check(s["recordingSHA256"] == v.Digest(source, "raw/raw.jsonl"), "summary recording hash differs")
	if plan["provider"] != "claude" {
		return
	}
	v.Check(v.Digest(replay, "materialization.json") == s["materializationSHA256"], "runtime materialization hash differs")
	m := v.Read(replay, "materialization.json")
	execution := tool.Str(m["executionProject"])
	output := tool.Str(s["output"])
	v.Check((execution == filepath.Join(output, "project") || execution == filepath.Join(output, "workspace")) &&
		m["originalProject"] == plan["project"] && m["originalHistoryProject"] == plan["historyProject"], "runtime project mapping differs")
	for _, item := range tool.Array(m["transformations"]) {
		x := tool.Obj(item)
		cwd := tool.Str(x["sourceCWD"])
		v.Check(x["kind"] == "claude-project-cwd-copy" && (x["sourceCWD"] == plan["project"] || x["sourceCWD"] == plan["historyProject"]) && x["executionCWD"] == execution, "unsupported runtime transformation")
		prefix := "config/projects/" + claudeProjectKey(cwd) + "/"
		target := "config/projects/" + claudeProjectKey(execution) + "/"
		v.Check(strings.HasPrefix(tool.Str(x["source"]), prefix) && x["derived"] == target+strings.TrimPrefix(tool.Str(x["source"]), prefix), "runtime history ownership differs")
		v.Hashes(source, tool.Object{tool.Str(x["source"]): x["sourceSHA256"]})
		v.Hashes(replay, tool.Object{tool.Str(x["derived"]): x["derivedSHA256"]})
	}
}

func (v *Validation) ReplayEvidence(replay string, plan tool.Object) tool.Object {
	var evidence tool.Object
	if tool.ReadJSON(filepath.Join(replay, "verification.json"), &evidence) == nil && evidence["pipeline"] == "public-agent-mock" {
		names := corpusSet([]string{"verification.json"})
		for name := range tool.Obj(evidence["files"]) {
			names[name] = true
		}
		return names
	}

	names := corpusSet([]string{"summary.json", "result.json", "replay-report.json"})
	var summary tool.Object
	if tool.ReadJSON(filepath.Join(replay, "summary.json"), &summary) == nil && summary["mode"] == "verify" {
		names["verification.json"] = true
		for _, phase := range Phases(plan) {
			name := "main"
			if phase[0] != "" {
				name = "probe"
			}
			names["peer-"+name+"-lifecycle.json"] = true
		}
	}
	for _, phase := range Phases(plan) {
		names[phase[0]+"replay-peer.json"] = true
	}
	if plan["provider"] == "claude" {
		names["materialization.json"] = true
		for _, item := range tool.Array(v.Read(replay, "materialization.json")["transformations"]) {
			names[tool.Str(tool.Obj(item)["derived"])] = true
		}
	}
	return names
}

func (v *Validation) Required(source string, plan tool.Object) tool.Object {
	names := corpusSet(SourceEvidence)
	if publicCapture(source) {
		delete(names, "result.json")
		delete(names, "summary.json")
		names["source-plan.json"], names["official-origin.json"] = true, true
		origin := v.Read(source, "official-origin.json")
		for _, id := range tool.Strings(origin["connections"]) {
			if Local(id) {
				names["origin/"+id+".json"] = true
			} else {
				v.Check(false, "invalid source connection identity")
			}
		}
	}
	manifest := v.Read(source, "manifest.json")
	for _, group := range []string{"savedFiles", "caseFiles", "remotePhaseFiles", "driverFiles"} {
		hashes := tool.Obj(manifest[group])
		if len(hashes) > 0 {
			v.Hashes(source, hashes)
			for name := range hashes {
				names[name] = true
			}
		}
	}
	for _, phase := range Phases(plan) {
		for _, name := range []string{"raw.jsonl", "manifest.json", "transport.json", "stderr.raw"} {
			names[phase[1]+"raw/"+name] = true
		}
	}
	for _, prefix := range []string{"config/projects", "config/sessions", "history-seed"} {
		p := filepath.Join(source, prefix)
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		}
		for name := range v.Inventory(p, true) {
			names[prefix+"/"+name] = true
		}
	}
	for name, evidence := range tool.Obj(tool.Obj(plan["historySeed"])["files"]) {
		v.Hashes(source, tool.Object{name: tool.Obj(evidence)["sha256"]})
		names[name] = true
	}
	if plan["driverProfile"] == "remote-close" {
		names["remote-service.json"] = true
	}
	return names
}
