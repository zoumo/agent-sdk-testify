package protocol

import (
	"fmt"
	"os"
	"testing"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGENT_MOCK_RECORD_TEST_ROLE") != "" {
		os.Exit(m.Run())
	}
	cleanup, err := testutil.PrepareBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
