package history

import (
	"bytes"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// ValidateMaterialization checks sealed transformations without reopening their
// former native workspace. All file references are relative to one package.
func ValidateMaterialization(packageRoot, execution string) error {
	var receipt tool.Object
	if err := tool.ReadJSON(filepath.Join(execution, "materialization.json"), &receipt); err != nil {
		return err
	}
	if receipt["version"] != float64(2) {
		return errors.New("unsupported history materialization version")
	}
	values, ok := receipt["transformations"].([]any)
	if !ok {
		return errors.New("history transformations must be an array")
	}
	seen := map[string]bool{}
	for _, value := range values {
		tr := tool.Obj(value)
		source, derived := tool.Str(tr["source"]), tool.Str(tr["derived"])
		target := tool.Str(tr["nativeTarget"])
		from, to := tool.Str(tr["sourceCWD"]), tool.Str(tr["executionCWD"])
		if tr["kind"] != "claude-project-cwd-copy" || !nativeTarget(target) || !strings.HasPrefix(target, "config/projects/") ||
			from == "" || to == "" || from == to || seen[target] {
			return errors.New("invalid sealed history transformation")
		}
		seen[target] = true
		if !strings.HasPrefix(source, "material/") && !strings.HasPrefix(source, "executions/"+filepath.Base(execution)+"/capture/") {
			return errors.New("sealed history source is outside its execution or material")
		}
		if derived != path.Join("executions", filepath.Base(execution), "history/derived", target) {
			return errors.New("sealed history derived reference differs from native target")
		}
		if err := checkLocalFile(packageRoot, source, tool.Str(tr["sourceSHA256"])); err != nil {
			return err
		}
		if err := checkLocalFile(packageRoot, derived, tool.Str(tr["derivedSHA256"])); err != nil {
			return err
		}
		key := regexp.MustCompile(`[^a-zA-Z0-9]`)
		prefix := "config/projects/" + key.ReplaceAllString(to, "-") + "/"
		if !strings.HasPrefix(target, prefix) {
			return errors.New("sealed history target differs from execution project")
		}
		sourceTarget := "config/projects/" + key.ReplaceAllString(from, "-") + "/" + strings.TrimPrefix(target, prefix)
		if !strings.HasSuffix(source, "/"+sourceTarget) {
			return errors.New("sealed history source differs from original native target")
		}
		data, err := os.ReadFile(filepath.Join(packageRoot, filepath.FromSlash(source)))
		if err != nil {
			return err
		}
		expected, err := claudeDerivedBytes(data, target, from, to)
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(filepath.Join(packageRoot, filepath.FromSlash(derived)))
		if err != nil {
			return err
		}
		if !bytes.Equal(expected, actual) {
			return errors.New("sealed history bytes differ from declared transformation")
		}
	}
	return nil
}
