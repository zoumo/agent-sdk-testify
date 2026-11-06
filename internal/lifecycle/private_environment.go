package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const privateEnvironmentName = "runtime.env.private.json"

func writePrivateEnvironment(root string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	return tool.WriteJSON(filepath.Join(root, privateEnvironmentName), values)
}

func readPrivateEnvironment(root string) (map[string]string, error) {
	path := filepath.Join(root, privateEnvironmentName)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("private runtime environment must be a private regular file")
	}
	var values map[string]string
	if err := tool.ReadJSON(path, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func prepareExecutionAuthentication(session *Session, action, root string) error {
	profile, err := agentProfileFor(session.AgentType)
	if err != nil {
		return err
	}
	config := filepath.Join(root, "config")
	for _, name := range profile.authFiles {
		if err := os.RemoveAll(filepath.Join(config, name)); err != nil {
			return err
		}
	}
	if action != "record" {
		return nil
	}
	for _, name := range profile.authFiles {
		source := filepath.Join(session.ResourceRoot, "private-config", name)
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("private authentication file must be a private regular file")
		}
		if err := os.MkdirAll(config, 0o700); err != nil {
			return err
		}
		if err := os.Symlink(source, filepath.Join(config, name)); err != nil {
			return err
		}
	}
	return nil
}

func scrubExecutionAuthentication(session *Session, overlayRoot string) error {
	profile, err := agentProfileFor(session.AgentType)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range profile.authFiles {
		if err := os.RemoveAll(filepath.Join(overlayRoot, "config", name)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func environmentNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func executionRuntimeEnvironment(session *Session, action, root string) (map[string]string, error) {
	env := baseEnvironment(session.AgentType, root)
	if action != "record" {
		return env, nil
	}
	private, err := readPrivateEnvironment(session.ResourceRoot)
	if err != nil {
		return nil, err
	}
	for _, name := range session.Routes[action].RuntimeEnv {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		} else if value, ok := private[name]; ok {
			env[name] = value
		}
	}
	return env, nil
}
