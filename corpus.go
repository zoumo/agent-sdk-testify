package main

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

	"github.com/zoumo/agent-sdk-testify/protocol/rawjson"
)

// Corpus policy belongs to the Go tool, not to an SDK driver. Paths are explicit
// so the compiled binary can operate outside its source checkout.
type corpus struct {
	Index, Scenarios, Archive string
}

var sourceEvidence = []string{"plan.json", "manifest.json", "report.json", "result.json", "summary.json"}

func claudeProjectKey(cwd string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-")
}

func corpusLocal(name string) bool {
	return name != "" && name != "." && !strings.Contains(name, "\\") && path.Clean(name) == name && filepath.IsLocal(name)
}

func corpusRegular(root, name string) (string, error) {
	if !corpusLocal(name) {
		return "", fmt.Errorf("non-local or non-canonical material path: %q", name)
	}
	p := root
	for _, part := range strings.Split(name, "/") {
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

func corpusDigest(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func corpusInventory(root string, empty bool) (object, error) {
	i, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("material directory is missing or linked")
	}
	out := object{}
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
		h, err := corpusDigest(p)
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

func corpusRead(p string) (object, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if _, err = rawjson.Parse(b); err != nil {
		return nil, err
	}
	var v object
	err = json.Unmarshal(b, &v)
	if err == nil && v == nil {
		err = errors.New("JSON evidence must be an object")
	}
	return v, err
}

func corpusWrite(p string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Validation keeps the first error. Subsequent reads/checks cannot turn a
// missing or malformed artifact into a successful qualification.
type corpusValidation struct{ err error }

func (v *corpusValidation) check(ok bool, message string) {
	if v.err == nil && !ok {
		v.err = errors.New(message)
	}
}
func (v *corpusValidation) read(root, name string) object {
	if v.err != nil {
		return object{}
	}
	p, err := corpusRegular(root, name)
	if err != nil {
		v.err = err
		return object{}
	}
	m, err := corpusRead(p)
	v.err = err
	return m
}
func (v *corpusValidation) digest(root, name string) string {
	if v.err != nil {
		return ""
	}
	p, err := corpusRegular(root, name)
	if err != nil {
		v.err = err
		return ""
	}
	h, err := corpusDigest(p)
	v.err = err
	return h
}
func (v *corpusValidation) hashes(root string, hashes object) {
	v.check(len(hashes) > 0, "nonempty hash inventory required")
	for name, expected := range hashes {
		v.check(len(str(expected)) == 64 && v.digest(root, name) == expected, "material hash mismatch: "+name)
	}
}
func (v *corpusValidation) inventory(root string, empty bool) object {
	if v.err != nil {
		return object{}
	}
	x, err := corpusInventory(root, empty)
	v.err = err
	return x
}
func corpusKeys(m object) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func corpusSet(names []string) object {
	m := object{}
	for _, n := range names {
		m[n] = true
	}
	return m
}
func corpusWithin(p, root string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && (r == "." || filepath.IsLocal(r))
}

func (c corpus) catalog() (object, error) {
	v := &corpusValidation{}
	index := v.read(c.Scenarios, "index.json")
	out := object{}
	for _, story := range stringsOf(index["stories"]) {
		s := v.read(c.Scenarios, story+".json")
		for _, item := range array(s["cases"]) {
			definition := obj(item)
			id := str(definition["id"])
			for _, provider := range []string{"codex", "claude"} {
				binding := v.read(c.Scenarios, "bindings/"+provider+".json")
				mode := str(obj(definition["execution"])["mode"])
				if x := str(obj(obj(obj(binding["cases"])[id])["execution"])["mode"]); x != "" {
					mode = x
				}
				required, ok := definition["required"].(bool)
				v.check(ok, "case requirement must be explicit")
				if override, exists := obj(obj(binding["cases"])[id])["required"]; exists {
					required, ok = override.(bool)
					v.check(ok, "provider requirement must be boolean")
				}
				requirement := "optional"
				if required {
					requirement = "required"
				}
				out[provider+"/"+id] = object{"story": story, "requirement": requirement, "route": mode, "driverProfile": definition["driverProfile"]}
			}
		}
	}
	return out, v.err
}

func (c corpus) loadIndex() (object, error) {
	index, err := corpusRead(c.Index)
	if err != nil {
		return nil, err
	}
	expected, err := c.catalog()
	if err != nil {
		return nil, err
	}
	v := &corpusValidation{}
	v.check(index["version"] == float64(1), "unsupported corpus index version")
	seen := map[string]bool{}
	for _, item := range array(index["cases"]) {
		e := obj(item)
		key := str(e["provider"]) + "/" + str(e["caseId"])
		target, ok := expected[key]
		v.check(ok && !seen[key], "unknown or duplicate corpus identity")
		seen[key] = true
		t := obj(target)
		v.check(e["storyId"] == t["story"] && e["requirement"] == t["requirement"], "corpus requirement differs from catalog")
		state := str(e["adoption"])
		v.check(state == "" || state == "missing" || state == "untriggered" || state == "admitted", "invalid adoption state")
		verification := str(e["verification"])
		v.check(corpusSet([]string{"passed", "failed", "unverified", "blocked", "unsupported"})[verification] == true, "invalid verification state")
		v.check(verification == "passed" || strings.TrimSpace(str(e["verificationReason"])) != "", "non-passing verification needs a reason")
		v.check(verification != "passed" || state == "admitted", "passing verification requires admitted evidence")
	}
	v.check(len(seen) > 0 && len(seen) == len(expected), "corpus index omits required identities")
	return index, v.err
}

func corpusPublishable(name string) bool {
	if !corpusLocal(name) {
		return false
	}
	allowed := corpusSet(append(append([]string{}, sourceEvidence...), "remote-service.json"))[name] == true
	for _, prefix := range []string{"raw/", "history-seed/", "driver/", "config/projects/", "config/sessions/", "remote-probe/raw/"} {
		allowed = allowed || strings.HasPrefix(name, prefix)
	}
	for _, forbidden := range []string{"credentials", "auth.json", ".private", ".env"} {
		if strings.Contains(strings.ToLower(name), forbidden) {
			return false
		}
	}
	return allowed
}

func corpusPhases(plan object, mode string) [][2]string {
	out := [][2]string{{"", ""}}
	if plan["driverProfile"] == "remote-close" {
		out = append(out, [2]string{"remote-probe/", "remote-probe/"})
	}
	return out
}

func (v *corpusValidation) summary(root, mode string, plan object) object {
	s := v.read(root, "summary.json")
	v.check(s["mode"] == mode && s["provider"] == plan["provider"] && s["case"] == plan["caseId"], "run identity or mode differs")
	v.check(s["error"] == nil && obj(s["result"])["status"] == "passed", "run did not pass")
	v.check(v.read(root, "result.json")["status"] == "passed", "business result did not pass")
	return s
}

func (v *corpusValidation) replay(source, replay string, plan object, mode string) {
	actualMode := mode
	if mode == "replay" {
		var summary object
		if readJSON(filepath.Join(replay, "summary.json"), &summary) == nil && summary["mode"] == "verify" {
			actualMode = "verify"
			attestation := v.read(replay, "verification.json")
			v.check(attestation["schemaVersion"] == float64(1) && attestation["status"] == "passed" && attestation["error"] == nil, "official verification did not pass")
			v.hashes(replay, obj(attestation["files"]))
			for _, name := range []string{"summary.json", "result.json", "replay-report.json"} {
				v.check(obj(attestation["files"])[name] != nil, "verification omits evidence hash")
			}
			for _, phase := range corpusPhases(plan, "verify") {
				name := "main"
				if phase[0] != "" {
					name = "probe"
				}
				lifecycle := "peer-" + name + "-lifecycle.json"
				v.check(obj(attestation["files"])[phase[0]+"replay-peer.json"] != nil && obj(attestation["files"])[lifecycle] != nil, "verification omits selected connection")
			}
		}
	}
	s := v.summary(replay, actualMode, plan)
	original := object{}
	for _, name := range sourceEvidence {
		original[name] = v.digest(source, name)
	}
	v.check(reflect.DeepEqual(s["source"], original), "run does not bind original source plan and manifest")
	for _, phase := range corpusPhases(plan, mode) {
		r := v.read(replay, phase[0]+"replay-peer.json")
		raw := phase[1] + "raw/raw.jsonl"
		v.check(r["provider"] == plan["provider"] && r["recordingSHA256"] == v.digest(source, raw) && r["fullConsumption"] == true && r["clientEOF"] == true && r["nativeLaunched"] == false && r["error"] == nil, "incomplete or incorrect mock receipt")
		if v.err == nil {
			b, err := os.ReadFile(filepath.Join(source, raw))
			v.err = err
			v.check(r["frames"] == float64(len(bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")))), "receipt frame count differs")
		}
	}
	v.check(s["recordingSHA256"] == v.digest(source, "raw/raw.jsonl"), "summary recording hash differs")
	if plan["provider"] != "claude" {
		return
	}
	v.check(v.digest(replay, "materialization.json") == s["materializationSHA256"], "runtime materialization hash differs")
	m := v.read(replay, "materialization.json")
	execution := str(m["executionProject"])
	v.check(execution == filepath.Join(str(s["output"]), "project") && m["originalProject"] == plan["project"] && m["originalHistoryProject"] == plan["historyProject"], "runtime project mapping differs")
	for _, item := range array(m["transformations"]) {
		x := obj(item)
		cwd := str(x["sourceCWD"])
		v.check(x["kind"] == "claude-project-cwd-copy" && (x["sourceCWD"] == plan["project"] || x["sourceCWD"] == plan["historyProject"]) && x["executionCWD"] == execution, "unsupported runtime transformation")
		prefix := "config/projects/" + claudeProjectKey(cwd) + "/"
		target := "config/projects/" + claudeProjectKey(execution) + "/"
		v.check(strings.HasPrefix(str(x["source"]), prefix) && x["derived"] == target+strings.TrimPrefix(str(x["source"]), prefix), "runtime history ownership differs")
		v.hashes(source, object{str(x["source"]): x["sourceSHA256"]})
		v.hashes(replay, object{str(x["derived"]): x["derivedSHA256"]})
	}
}

func (v *corpusValidation) replayEvidence(replay string, plan object) object {
	names := corpusSet([]string{"summary.json", "result.json", "replay-report.json"})
	var summary object
	if readJSON(filepath.Join(replay, "summary.json"), &summary) == nil && summary["mode"] == "verify" {
		names["verification.json"] = true
		for _, phase := range corpusPhases(plan, "verify") {
			name := "main"
			if phase[0] != "" {
				name = "probe"
			}
			names["peer-"+name+"-lifecycle.json"] = true
		}
	}
	for _, phase := range corpusPhases(plan, "replay") {
		names[phase[0]+"replay-peer.json"] = true
	}
	if plan["provider"] == "claude" {
		names["materialization.json"] = true
		for _, item := range array(v.read(replay, "materialization.json")["transformations"]) {
			names[str(obj(item)["derived"])] = true
		}
	}
	return names
}

func (v *corpusValidation) required(source string, plan object) object {
	names := corpusSet(sourceEvidence)
	manifest := v.read(source, "manifest.json")
	for _, group := range []string{"savedFiles", "caseFiles", "remotePhaseFiles", "driverFiles"} {
		hashes := obj(manifest[group])
		if len(hashes) > 0 {
			v.hashes(source, hashes)
			for name := range hashes {
				names[name] = true
			}
		}
	}
	for _, phase := range corpusPhases(plan, "replay") {
		for _, name := range []string{"raw.jsonl", "manifest.json", "transport.json", "stderr.raw"} {
			names[phase[1]+"raw/"+name] = true
		}
	}
	for _, prefix := range []string{"config/projects", "config/sessions", "history-seed"} {
		p := filepath.Join(source, prefix)
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		}
		for name := range v.inventory(p, true) {
			names[prefix+"/"+name] = true
		}
	}
	for name, evidence := range obj(obj(plan["historySeed"])["files"]) {
		v.hashes(source, object{name: obj(evidence)["sha256"]})
		names[name] = true
	}
	if plan["driverProfile"] == "remote-close" {
		names["remote-service.json"] = true
	}
	return names
}
