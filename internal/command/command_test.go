package command

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
)

func TestRuntimeErrorClassification(t *testing.T) {
	for _, code := range []int{exitcode.Failure, exitcode.Configuration, exitcode.Canceled, exitcode.Disconnect, exitcode.Unimplemented} {
		err := runtimeError(exitcode.Fail(code, errors.New("classified")))
		require.Equal(t, code, exitcode.Code(err))
	}
	require.Equal(t, exitcode.Canceled, exitcode.Code(runtimeError(context.DeadlineExceeded)))
	require.Equal(t, exitcode.Failure, exitcode.Code(runtimeError(errors.New("business failure"))))
}
