package verify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIRejectsMissingSelection(t *testing.T) {
	err := Run(context.Background(), Options{})
	require.Error(t, err)
	require.ErrorContains(t, err, "session-dir")
}
