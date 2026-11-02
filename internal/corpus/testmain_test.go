package corpus

import (
	"fmt"
	"os"
	"testing"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "verify-driver-substitute" {
		os.Exit(verifyDriverSubstitute(os.Args[2:]))
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
