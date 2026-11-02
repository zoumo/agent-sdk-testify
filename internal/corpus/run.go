package corpus

import (
	"errors"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func DefaultArchive() (string, error) {
	base, err := tool.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "archives"), nil
}

func (c *Corpus) Normalize() error {
	if c.Index == "" {
		return errors.New("corpus index is required")
	}
	var err error
	c.Index, err = filepath.Abs(c.Index)
	if err != nil {
		return err
	}
	if c.Scenarios == "" {
		c.Scenarios = filepath.Join(filepath.Dir(c.Index), "scenarios")
	}
	c.Scenarios, err = filepath.Abs(c.Scenarios)
	if err != nil {
		return err
	}
	c.Archive, err = c.archiveRoot()
	return err
}
