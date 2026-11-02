package verify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIRejectsMissingSelection(t *testing.T) {
	err := Execute(context.Background(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "recording")
}
