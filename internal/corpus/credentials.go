package corpus

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// The scan describes this capture only. It never sanitizes or rewrites evidence.
func ScanCaptureCredentials(root string) (string, error) {
	var env map[string]string
	if err := tool.ReadJSON(filepath.Join(root, "native.env.private.json"), &env); err != nil {
		return "unavailable", err
	}
	return ScanCaptureCredentialsWithEnvironment(root, env)
}

// ScanCaptureCredentialsWithEnvironment is for prepared actions whose selected
// environment is already represented by a descriptor and must not be copied
// into material as a private native.env file.
func ScanCaptureCredentialsWithEnvironment(root string, env map[string]string) (string, error) {
	return scanCaptureCredentials(root, env, nil, nil)
}

// ScanPiCaptureCredentials also checks saved session history for credentials
// from the isolated Pi auth and model stores.
func ScanPiCaptureCredentials(root, config string, env map[string]string) (string, error) {
	var secrets []string
	var modelAliases []string
	appendSecret := func(value any) {
		if text := tool.Str(value); text != "" {
			secrets = append(secrets, text)
		}
	}
	var auth tool.Object
	if err := tool.ReadJSON(filepath.Join(config, "auth.json"), &auth); err == nil {
		for _, value := range auth {
			credential := tool.Obj(value)
			for _, key := range []string{"key", "access", "refresh"} {
				appendSecret(credential[key])
			}
		}
	} else if !os.IsNotExist(err) {
		return "unavailable", err
	}
	var models tool.Object
	if err := tool.ReadJSON(filepath.Join(config, "models.json"), &models); err == nil {
		addModelCredentials := func(settings tool.Object) {
			appendSecret(settings["apiKey"])
			for _, value := range tool.Obj(settings["headers"]) {
				appendSecret(value)
			}
		}
		for name, value := range tool.Obj(models["providers"]) {
			provider := tool.Obj(value)
			if name != "" && tool.Str(provider["apiKey"]) == name {
				// Some model routes use the provider name as the API key. The
				// name also appears in Pi's typed routing metadata.
				modelAliases = append(modelAliases, name)
				for _, header := range tool.Obj(provider["headers"]) {
					appendSecret(header)
				}
			} else {
				addModelCredentials(provider)
			}
			for _, model := range tool.Array(provider["models"]) {
				addModelCredentials(tool.Obj(model))
			}
			for _, override := range tool.Obj(provider["modelOverrides"]) {
				addModelCredentials(tool.Obj(override))
			}
		}
	} else if !os.IsNotExist(err) {
		return "unavailable", err
	}
	scan, err := scanCaptureCredentials(root, env, secrets, modelAliases)
	if err != nil {
		return scan, err
	}
	history := filepath.Join(root, "history")
	err = filepath.WalkDir(history, func(path string, entry fs.DirEntry, err error) error {
		if err != nil && path == history && os.IsNotExist(err) {
			return nil
		}
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		return checkCredentialValues(path, root, secrets, modelAliases)
	})
	if errors.Is(err, errCaptureCredentials) {
		return "quarantined", err
	}
	if err != nil {
		return "unavailable", err
	}
	return "pass", nil
}

func scanCaptureCredentials(root string, env map[string]string, secrets, modelAliases []string) (string, error) {
	for key, value := range env {
		if value != "" && (strings.Contains(key, "TOKEN") || strings.Contains(key, "KEY")) {
			secrets = append(secrets, value)
		}
	}
	var credentials func(any)
	credentials = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, item := range value {
				name := strings.ToLower(key)
				if text, ok := item.(string); ok && text != "" && (strings.Contains(name, "token") || strings.Contains(name, "api_key") || strings.Contains(name, "apikey")) {
					secrets = append(secrets, text)
				}
				credentials(item)
			}
		case []any:
			for _, item := range value {
				credentials(item)
			}
		}
	}
	for _, name := range []string{"config/auth.json", "config/.credentials.json"} {
		var value tool.Object
		if err := tool.ReadJSON(filepath.Join(root, name), &value); err == nil {
			credentials(value)
		} else if !os.IsNotExist(err) {
			return "unavailable", err
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "config", "config.toml"))
	if err == nil {
		var config tool.Object
		if err := toml.Unmarshal(data, &config); err != nil {
			return "unavailable", err
		}
		provider := tool.Obj(tool.Obj(config["model_providers"])[tool.Str(config["model_provider"])])
		names := []string{tool.Str(provider["env_key"])}
		for _, value := range tool.Obj(provider["env_http_headers"]) {
			names = append(names, tool.Str(value))
		}
		for _, name := range names {
			if env[name] != "" {
				secrets = append(secrets, env[name])
			}
		}
		for _, value := range tool.Obj(provider["http_headers"]) {
			if tool.Str(value) != "" {
				secrets = append(secrets, tool.Str(value))
			}
		}
	} else if !os.IsNotExist(err) {
		return "unavailable", err
	}
	for _, pattern := range []string{"raw/*", "remote-probe/raw/*", "*report.json", "*diagnostics.private.json", "stderr.log", "stdout.log", "remote-service.json"} {
		paths, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return "unavailable", err
		}
		for _, path := range paths {
			if err := checkCredentialValues(path, root, secrets, modelAliases); errors.Is(err, errCaptureCredentials) {
				return "quarantined", err
			} else if err != nil {
				return "unavailable", err
			}
		}
	}
	return "pass", nil
}

var errCaptureCredentials = errors.New("capture contains selected credentials; private evidence quarantined")

func checkCredentialValues(path, root string, secrets, modelAliases []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			return errCaptureCredentials
		}
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	for _, alias := range modelAliases {
		if count := bytes.Count(data, []byte(alias)); count > 0 && count != piModelAliasMetadata(filepath.ToSlash(rel), data, alias) {
			return errCaptureCredentials
		}
	}
	return nil
}
