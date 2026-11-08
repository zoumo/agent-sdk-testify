package official

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyRejectsMissingFixture(t *testing.T) {
	err := Verify(context.Background(), VerifyOptions{})
	require.Error(t, err)
	require.ErrorContains(t, err, "fixture-dir")
}
