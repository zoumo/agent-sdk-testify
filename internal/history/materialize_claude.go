package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/zoumo/agent-sdk-testify/protocol/rawjson"
)

// Claude's public process/history APIs require an existing canonical WorkDir.
// Keep original native identities and storage untouched, and explicitly derive
// additional project-key/cwd copies for this attempt's isolated workspace.
func MaterializeClaude(source, root, workspace string, plan object) error {
	key := regexp.MustCompile(`[^a-zA-Z0-9]`)
	targetKey := key.ReplaceAllString(workspace, "-")
	var transformations []object
	seen := map[string]bool{}
	for _, cwd := range []string{str(plan["project"]), str(plan["historyProject"])} {
		if cwd == "" || seen[cwd] {
			continue
		}
		seen[cwd] = true
		prefix := filepath.Join("config/projects", key.ReplaceAllString(cwd, "-"))
		original := filepath.Join(source, prefix)
		if _, err := os.Stat(original); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		err := filepath.WalkDir(original, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(original, path)
			if err != nil {
				return err
			}
			derived := filepath.Join("config/projects", targetKey, rel)
			if entry.IsDir() {
				return os.MkdirAll(filepath.Join(root, derived), 0700)
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("non-regular Claude history material")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			output := data
			if filepath.Ext(path) == ".jsonl" || filepath.Ext(path) == ".json" {
				var result bytes.Buffer
				rows := bytes.SplitAfter(data, []byte{'\n'})
				if filepath.Ext(path) == ".json" {
					rows = [][]byte{data}
				}
				for _, line := range rows {
					if len(bytes.TrimSpace(line)) == 0 {
						result.Write(line)
						continue
					}
					record, err := rawjson.Parse(line)
					if err != nil || record.Kind() != rawjson.Object {
						return fmt.Errorf("invalid Claude history derivation source: %s", path)
					}
					if value, ok := record.Member("cwd"); ok && value.Kind() != rawjson.Null {
						recordedCWD, err := value.String()
						if err != nil {
							return err
						}
						if recordedCWD != "" && recordedCWD != cwd {
							return fmt.Errorf("Claude history cwd differs from original project")
						}
						if recordedCWD == cwd {
							encoded, _ := json.Marshal(workspace)
							replacement, err := rawjson.Parse(encoded)
							if err != nil {
								return err
							}
							record, err = record.Replace([]string{"cwd"}, replacement)
							if err != nil {
								return err
							}
							line = record.Raw()
						}
					}
					result.Write(line)
				}
				output = result.Bytes()
			}
			f, err := os.OpenFile(filepath.Join(root, derived), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err // Never overwrite an original or merge ambiguous projects.
			}
			_, writeErr := f.Write(output)
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
			hash := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
			transformations = append(transformations, object{"kind": "claude-project-cwd-copy", "source": filepath.Join(prefix, rel), "sourceSHA256": hash(data), "derived": derived, "derivedSHA256": hash(output), "sourceCWD": cwd, "executionCWD": workspace})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(root, "materialization.json"), object{"version": 1, "executionProject": workspace, "originalProject": plan["project"], "originalHistoryProject": plan["historyProject"], "transformations": transformations})
}
