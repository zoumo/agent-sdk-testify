package official

import (
	"maps"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func driverEnvironment(o Options, plan tool.Object) ([]string, error) {
	if len(o.RuntimeEnv) > 0 {
		var values []string
		for k, v := range o.RuntimeEnv {
			values = append(values, k+"="+v)
		}
		return values, nil
	}
	env := map[string]string{"PATH": os.Getenv("PATH"), "HOME": tool.Str(plan["home"]), "TMPDIR": filepath.Join(tool.Str(plan["root"]), "tmp"), "LANG": "C.UTF-8"}
	if tool.Str(plan["provider"]) == "codex" {
		env["CODEX_HOME"] = tool.Str(plan["config"])
	} else {
		env["CLAUDE_CONFIG_DIR"] = tool.Str(plan["config"])
		env["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] = "1"
		env["DISABLE_AUTOUPDATER"] = "1"
	}
	if _, err := os.Stat(filepath.Join(tool.Str(plan["root"]), "native.env.private.json")); err == nil {
		var selected map[string]string
		if err := tool.ReadJSON(filepath.Join(tool.Str(plan["root"]), "native.env.private.json"), &selected); err != nil {
			return nil, err
		}
		maps.Copy(env, selected)
	}
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out, nil
}
