package capture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunRejectsInvalidConfigurationWithoutCreatingOutput(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*Options)
	}{
		{"missing executable", func(o *Options) { o.Command = nil }},
		{"invalid cleanup", func(o *Options) { o.CleanupTimeout = 0 }},
		{"invalid limit", func(o *Options) { o.MaxFrameBytes = -1 }},
		{"unknown EOF action", func(o *Options) { o.StdinEOFAction = "discard" }},
		{"duplicate environment", func(o *Options) { o.Env = []string{"X=one", "X=two"} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "capture")
			o := Options{Output: output, Command: []string{"unused"}, CleanupTimeout: time.Second, StdinEOFAction: "close"}
			change.apply(&o)
			err := Run(context.Background(), o, IO{})
			var failure *Error
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, Configuration, failure.Kind)
			_, err = os.Stat(output)
			assert.True(t, errors.Is(err, os.ErrNotExist))
		})
	}
}
