package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// The scan describes this capture only. It never sanitizes or rewrites evidence.
func scanCaptureCredentials(root string) (string, error) {
	var env map[string]string
	if err := readJSON(filepath.Join(root, "native.env.private.json"), &env); err != nil {
		return "unavailable", err
	}
	var secrets []string
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
		var value object
		if err := readJSON(filepath.Join(root, name), &value); err == nil {
			credentials(value)
		} else if !os.IsNotExist(err) {
			return "unavailable", err
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "config/config.toml"))
	if err == nil {
		var config object
		if err = toml.Unmarshal(data, &config); err != nil {
			return "unavailable", err
		}
		provider := obj(obj(config["model_providers"])[str(config["model_provider"])])
		names := []string{str(provider["env_key"])}
		for _, value := range obj(provider["env_http_headers"]) {
			names = append(names, str(value))
		}
		for _, name := range names {
			if env[name] != "" {
				secrets = append(secrets, env[name])
			}
		}
		for _, value := range obj(provider["http_headers"]) {
			if str(value) != "" {
				secrets = append(secrets, str(value))
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
			data, err := os.ReadFile(path)
			if err != nil {
				return "unavailable", err
			}
			for _, secret := range secrets {
				if bytes.Contains(data, []byte(secret)) {
					return "quarantined", errors.New("capture contains selected credentials; private evidence quarantined")
				}
			}
		}
	}
	return "pass", nil
}
