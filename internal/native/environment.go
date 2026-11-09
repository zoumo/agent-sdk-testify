package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func PrepareNative(ctx context.Context, o *Options, plan tool.Object, root string) error {
	if o.CLI == "" {
		o.CLI = tool.Str(plan["cli"])
	}
	if o.CLI == "" {
		o.CLI = tool.Str(plan["provider"])
	}
	cli, err := tool.AbsoluteExecutable(o.CLI)
	if err != nil {
		return err
	}
	plan["cli"] = cli
	delete(plan, "cliVersion")
	if data, e := os.ReadFile(cli); e == nil {
		sum := sha256.Sum256(data)
		plan["cliSHA256"] = hex.EncodeToString(sum[:])
	} else {
		return e
	}
	if o.Model != "" {
		plan["model"] = o.Model
	}
	delete(plan, "initialModel")
	if o.InitialModel != "" {
		if o.InitialModel == tool.Str(plan["model"]) || tool.Str(plan["model"]) == "" {
			return errors.New("initial model requires a different explicit target model")
		}
		plan["initialModel"] = o.InitialModel
	}
	var version bytes.Buffer
	probe := exec.CommandContext(context.WithoutCancel(ctx), cli, "--version")
	probe.Stdout = &version
	probe.Dir = root
	var trackingErr error
	track := func(state string, pid int) error {
		if o.ProbeProcess != nil {
			trackingErr = o.ProbeProcess(state, pid)
		}
		return trackingErr
	}
	if err := track("launching", 0); err != nil {
		return err
	}
	probeErr := tool.WaitDriver(ctx, probe, func(pid int) error { return track("started", pid) })
	if probe.Process == nil {
		probeErr = errors.Join(probeErr, track("not-started", 0))
	}
	if probe.Process != nil {
		pid := probe.Process.Pid
		if err := tool.WaitProcessGroup(pid); err != nil {
			return errors.Join(probeErr, err)
		}
		if err := track("stopped", pid); err != nil {
			return errors.Join(probeErr, err)
		}
	}
	if trackingErr != nil {
		return errors.Join(probeErr, trackingErr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if probeErr == nil {
		plan["cliVersion"] = strings.TrimSpace(version.String())
	}
	provider := tool.Str(plan["provider"])
	profile := tool.Str(plan["driverProfile"])
	params := tool.Obj(tool.Obj(plan["scenario"])["parameters"])
	if o.InitialModel != "" {
		configuration := tool.Obj(tool.Obj(plan["scenario"])["configuration"])
		timing := tool.Str(params["configurationTiming"])
		modelSetting := params["setting"] == "model"
		if timing == "" && tool.Str(configuration["modelSource"]) != "" {
			timing, modelSetting = tool.Str(configuration["timing"]), true
		}
		if !modelSetting || (timing != "before-first-input" && timing != "between-inputs" && timing != "during-execution") {
			return errors.New("initial model is only valid for model-change recordings")
		}
	}
	args := tool.Strings(plan["args"])
	if len(args) == 0 {
		if provider == "codex" {
			args = codexNativeArgs(profile, params)
		} else {
			args, err = claudeNativeArgs(profile, params)
			if err != nil {
				return err
			}
		}
	} else if provider == "codex" && protocol.ValidateNativeArgs(provider, args) == nil {
		args = append(args, codexCaseArgs(profile, params)...)
	} else if provider == "claude" && protocol.ValidateNativeArgs(provider, args) == nil {
		caseArgs, err := claudeCaseArgs(profile, params)
		if err != nil {
			return err
		}
		args = append(args, caseArgs...)
	}
	if provider == "claude" {
		args, err = prepareClaudeArgs(plan, args, profile)
		if err != nil {
			return err
		}
	}
	plan["args"] = args
	plan["stdinEOFAction"] = "close"
	if provider == "claude" && profile == "interrupt" {
		plan["stdinEOFAction"] = "interrupt"
	}
	if profile == "children" || profile == "background-children" {
		if err := tool.WriteFiles(tool.Str(plan["project"]), tool.Object{"child-fixture.txt": "SYNTHETIC_CHILD_TOKEN\n"}); err != nil {
			return err
		}
		if provider == "codex" {
			if err := prepareCodexChildFixture(root); err != nil {
				return err
			}
		}
	}
	env := map[string]string{}
	if len(o.RuntimeEnv) > 0 {
		maps.Copy(env, o.RuntimeEnv)
	} else {
		for _, key := range nativeEnvironmentNames(provider) {
			if v, ok := os.LookupEnv(key); ok {
				env[key] = v
			}
		}
	}
	if o.Config != "" {
		files := []string{"config.toml", "auth.json"}
		if provider == "claude" {
			files = []string{"settings.json", ".credentials.json"}
		}
		configSource := filepath.Join(o.Config, "config.toml")
		configTarget := filepath.Join(root, "config", "config.toml")
		for _, name := range files {
			source := filepath.Join(o.Config, name)
			if _, e := os.Stat(source); os.IsNotExist(e) {
				continue
			} else if e != nil {
				return e
			}
			if err := tool.CopyFile(source, filepath.Join(root, "config", name)); err != nil {
				return err
			}
		}
		if provider == "claude" {
			claudeAuthEnvironment(filepath.Join(root, "config", "settings.json"), env)
			// The selected connection is carried in the isolated process env;
			// retain the native default model, excluding inherited integrations.
			if err := filterClaudeConfig(filepath.Join(root, "config", "settings.json")); err != nil {
				return err
			}
		} else {
			if err := filterCodexConfig(filepath.Join(root, "config", "config.toml")); err != nil {
				return err
			}
			if err := codexAuthEnvironment(filepath.Join(root, "config", "config.toml"), env); err != nil {
				return err
			}
			if err := copyCodexModelCatalog(configSource, configTarget); err != nil {
				return err
			}
		}
	}
	if provider == "codex" && params["nativeFileTool"] == "apply_patch" {
		if err := prepareCodexFileTool(root, plan); err != nil {
			return err
		}
	}
	if provider == "codex" {
		if err := prepareCodexIsolation(root, plan); err != nil {
			return err
		}
	}
	plan["recordingIsolation"] = isolatedRecording
	// No arbitrary source HOME, dependency installation or SDK version probes.
	return tool.WriteJSON(filepath.Join(root, "native.env.private.json"), env)
}

// CopyNativeConfig copies connection and model-routing configuration into the
// visible config directory. Authentication files stay in privateDestination so
// an execution overlay can link them for one record run without retaining them.
// The source is discovered from the standard environment or HOME location;
// callers cannot inject an arbitrary configuration path.
func CopyNativeConfig(provider, destination, privateDestination string, env map[string]string) error {
	if provider != "codex" && provider != "claude" {
		return errors.New("unsupported provider")
	}
	for _, key := range nativeEnvironmentNames(provider) {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	name, fallback := "CODEX_HOME", ".codex"
	if provider == "claude" {
		name, fallback = "CLAUDE_CONFIG_DIR", ".claude"
	}
	source, found := os.LookupEnv(name)
	if found {
		if !filepath.IsAbs(source) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		source = filepath.Join(home, fallback)
	}
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return os.MkdirAll(destination, 0o700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("native configuration source is not an unlinked directory: %s", source)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	configFile, authFile := "config.toml", "auth.json"
	if provider == "claude" {
		configFile, authFile = "settings.json", ".credentials.json"
	}
	configSource := filepath.Join(source, configFile)
	from := configSource
	info, err = os.Lstat(from)
	if os.IsNotExist(err) {
		info = nil
	} else if err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("native configuration file is not regular: %s", from)
	}
	if info != nil {
		if err := tool.CopyFile(from, filepath.Join(destination, configFile)); err != nil {
			return err
		}
	}
	from = filepath.Join(source, authFile)
	info, err = os.Lstat(from)
	if os.IsNotExist(err) {
		info = nil
	} else if err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("native authentication file is not regular: %s", from)
	}
	if info != nil {
		if err := tool.CopyFile(from, filepath.Join(privateDestination, authFile)); err != nil {
			return err
		}
	}
	if provider == "claude" {
		claudeAuthEnvironment(filepath.Join(destination, "settings.json"), env)
		return filterClaudeConfig(filepath.Join(destination, "settings.json"))
	}
	if err := codexAuthEnvironment(filepath.Join(destination, "config.toml"), env); err != nil {
		return err
	}
	if err := filterCodexConfig(filepath.Join(destination, "config.toml")); err != nil {
		return err
	}
	return copyCodexModelCatalog(configSource, filepath.Join(destination, "config.toml"))
}

func nativeEnvironmentNames(provider string) []string {
	names := []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	}
	switch provider {
	case "codex":
		names = append(names,
			"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY",
		)
	case "claude":
		names = append(names,
			"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
			"ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_OAUTH_TOKEN",
			"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
			"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
			"CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_AUTO_MODE_MODEL",
			"CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_CODE_MAX_CONTEXT_TOKENS",
			"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_MAX_OUTPUT_TOKENS",
			"CLAUDE_CODE_MODEL_CAPABILITIES", "CLAUDE_CODE_AUTO_MODE_SERVER",
			"CLAUDE_CODE_DISABLE_EXPLORE_INHERIT_CAP", "ENABLE_TOOL_SEARCH",
		)
		for _, tier := range []string{"OPUS", "SONNET", "HAIKU", "FABLE"} {
			for _, suffix := range []string{"_NAME", "_DESCRIPTION", "_SUPPORTED_CAPABILITIES"} {
				names = append(names, "ANTHROPIC_DEFAULT_"+tier+"_MODEL"+suffix)
			}
		}
	}
	return names
}

func copyCodexModelCatalog(sourceConfig, destinationConfig string) error {
	var source struct {
		Catalog string `toml:"model_catalog_json"`
	}
	data, err := os.ReadFile(sourceConfig)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, &source); err != nil {
		return err
	}
	if source.Catalog == "" {
		return nil
	}
	catalog := source.Catalog
	if !filepath.IsAbs(catalog) {
		catalog = filepath.Join(filepath.Dir(sourceConfig), catalog)
	}
	info, err := os.Lstat(catalog)
	if err != nil {
		return fmt.Errorf("codex model catalog: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("codex model catalog is not a regular file: %s", catalog)
	}
	target := filepath.Join(filepath.Dir(destinationConfig), "model-catalog.json")
	if err := tool.CopyFile(catalog, target); err != nil {
		return err
	}
	var destination tool.Object
	data, err = os.ReadFile(destinationConfig)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, &destination); err != nil {
		return err
	}
	destination["model_catalog_json"] = target
	data, err = toml.Marshal(destination)
	if err != nil {
		return err
	}
	return os.WriteFile(destinationConfig, data, 0o600)
}

func scrubNative(root string) error {
	var errs []error
	for _, name := range []string{"native.env.private.json", "config/auth.json", "config/.credentials.json", "config/config.toml", "config/settings.json"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
