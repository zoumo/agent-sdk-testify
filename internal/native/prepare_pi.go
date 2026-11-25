package native

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Pi model routes may contain inline credentials. Only record executions receive
// these two private files. Host extensions, settings and sessions stay outside the fixture.
func copyPiConfig(destination, private string, env map[string]string) error {
	for _, key := range nativeEnvironmentNames("pi") {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	source, set := os.LookupEnv("PI_CODING_AGENT_DIR")
	if !set {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		source = filepath.Join(home, ".pi", "agent")
	} else if !filepath.IsAbs(source) {
		return errors.New("PI_CODING_AGENT_DIR must be an absolute path")
	}
	dir, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !dir.IsDir() {
		return fmt.Errorf("native configuration source is not an unlinked directory: %s", source)
	}
	for _, name := range []string{"auth.json", "models.json"} {
		file := filepath.Join(source, name)
		info, err := os.Lstat(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("native configuration file is not regular: %s", file)
		}
		if err := tool.CopyFile(file, filepath.Join(private, name)); err != nil {
			return err
		}
	}
	return nil
}
