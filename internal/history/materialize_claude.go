package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/rawjson"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Claude's public process/history APIs require an existing canonical WorkDir.
// Keep original native identities and storage untouched, and explicitly derive
// additional project-key/cwd copies for this attempt's isolated workspace.
func MaterializeClaude(source, root, workspace string, plan tool.Object) error {
	key := regexp.MustCompile(`[^a-zA-Z0-9]`)
	targetKey := key.ReplaceAllString(workspace, "-")
	var transformations []tool.Object
	seen := map[string]bool{}
	for _, cwd := range []string{tool.Str(plan["project"]), tool.Str(plan["_fixtureProject"]), tool.Str(plan["historyProject"])} {
		if cwd == "" || seen[cwd] {
			continue
		}
		seen[cwd] = true
		prefix := filepath.Join("config", "projects", key.ReplaceAllString(cwd, "-"))
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
			derived := filepath.Join("config", "projects", targetKey, rel)
			if entry.IsDir() {
				return os.MkdirAll(filepath.Join(root, derived), 0o700)
			}
			if !entry.Type().IsRegular() {
				return errors.New("non-regular Claude history material")
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
							return errors.New("claude history cwd differs from original project")
						}
						if recordedCWD == cwd {
							encoded, err := json.Marshal(workspace)
							if err != nil {
								return fmt.Errorf("encode claude history workspace: %w", err)
							}
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
			f, err := os.OpenFile(filepath.Join(root, derived), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
			transformations = append(transformations, tool.Object{"kind": "claude-project-cwd-copy", "source": filepath.Join(prefix, rel), "sourceSHA256": hash(data), "derived": derived, "derivedSHA256": hash(output), "sourceCWD": cwd, "executionCWD": workspace})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return tool.WriteJSON(filepath.Join(root, "materialization.json"), tool.Object{"version": 1, "executionProject": workspace, "originalProject": plan["project"], "originalHistoryProject": plan["historyProject"], "transformations": transformations})
}

// deriveCanonicalClaude seals only bytes that require a cwd/project transform.
func deriveCanonicalClaude(source, root, workspace string, binding Binding) (tool.Object, error) {
	if !strings.HasPrefix(binding.NativeTarget, "config/projects/") {
		return nil, nil
	}
	key := regexp.MustCompile(`[^a-zA-Z0-9]`)
	prefix := "config/projects/" + key.ReplaceAllString(binding.Project, "-") + "/"
	if !strings.HasPrefix(binding.NativeTarget, prefix) {
		return nil, errors.New("claude native target differs from original project")
	}
	target := "config/projects/" + key.ReplaceAllString(workspace, "-") + "/" + strings.TrimPrefix(binding.NativeTarget, prefix)
	if target == binding.NativeTarget {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(binding.Source)))
	if err != nil {
		return nil, err
	}
	output, err := claudeDerivedBytes(data, target, binding.Project, workspace)
	if err != nil {
		return nil, err
	}
	derived := "history/derived/" + target
	for _, name := range []string{target, derived} {
		dest := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		_, we := f.Write(output)
		if err := errors.Join(we, f.Close()); err != nil {
			return nil, err
		}
	}
	return tool.Object{"kind": "claude-project-cwd-copy", "source": binding.Source, "sourceSHA256": binding.SHA256, "derived": derived, "derivedSHA256": tool.DigestBytes(output), "nativeTarget": target, "sourceCWD": binding.Project, "executionCWD": workspace}, nil
}

func claudeDerivedBytes(data []byte, target, sourceCWD, executionCWD string) ([]byte, error) {
	var output bytes.Buffer
	rows := bytes.SplitAfter(data, []byte{'\n'})
	if filepath.Ext(target) == ".json" {
		rows = [][]byte{data}
	}
	for _, line := range rows {
		if len(bytes.TrimSpace(line)) == 0 {
			output.Write(line)
			continue
		}
		record, err := rawjson.Parse(line)
		if err != nil || record.Kind() != rawjson.Object {
			return nil, errors.New("invalid Claude history derivation source")
		}
		if value, ok := record.Member("cwd"); ok && value.Kind() != rawjson.Null {
			cwd, err := value.String()
			if err != nil {
				return nil, err
			}
			if cwd != "" && cwd != sourceCWD {
				return nil, errors.New("claude history cwd differs from original project")
			}
			if cwd == sourceCWD {
				encoded, err := json.Marshal(executionCWD)
				if err != nil {
					return nil, err
				}
				replacement, err := rawjson.Parse(encoded)
				if err != nil {
					return nil, err
				}
				changed, err := record.Replace([]string{"cwd"}, replacement)
				if err != nil {
					return nil, err
				}
				// Preserve trailing bytes outside the parsed object, including newline.
				line = append(append([]byte(nil), changed.Raw()...), line[len(record.Raw()):]...)
			}
		}
		output.Write(line)
	}
	return output.Bytes(), nil
}
