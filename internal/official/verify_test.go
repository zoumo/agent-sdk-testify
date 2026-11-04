package official

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyRejectsMissingSession(t *testing.T) {
	err := Verify(context.Background(), VerifyOptions{})
	require.Error(t, err)
	require.ErrorContains(t, err, "session-dir")
}
