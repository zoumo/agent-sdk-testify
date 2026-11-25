package official

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func stagePiExtension(driver, private, caseID string) error {
	source := filepath.Join(filepath.Dir(driver), corpus.PiExtensionDirectory, corpus.PiExtensionFilename)
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if tool.DigestBytes(data) != corpus.PiExtensionSHA256 {
		return errors.New("installed Pi extension differs from its pinned Go SDK source")
	}
	dir := filepath.Join(private, corpus.PiExtensionDirectory)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, corpus.PiExtensionFilename), data, 0o600); err != nil {
		return err
	}
	return tool.WriteJSON(filepath.Join(dir, "session.json"), corpus.PiExtensionConfig(caseID))
}
