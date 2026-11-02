package main

import (
	"errors"
	"os"
	"path/filepath"
)

func corpusDefaultArchive() string {
	base := os.Getenv("AGENT_SDK_TESTIFY_DATA_DIR")
	if base == "" {
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
		base = filepath.Join(base, "agent-sdk-testify")
	}
	return filepath.Join(base, "archives")
}

func (c *corpus) normalize() error {
	if c.Index == "" {
		return errors.New("corpus index is required")
	}
	var err error
	c.Index, err = filepath.Abs(c.Index)
	if err != nil {
		return err
	}
	if c.Scenarios == "" {
		c.Scenarios = filepath.Join(filepath.Dir(filepath.Dir(c.Index)), "scenarios")
	}
	c.Scenarios, err = filepath.Abs(c.Scenarios)
	if err != nil {
		return err
	}
	c.Archive, err = c.archiveRoot()
	return err
}
