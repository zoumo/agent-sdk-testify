package mock

import (
	"fmt"
	"os"
	"testing"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "app-server":
			os.Exit(mockTestAgent())
		}
	}
	cleanup, err := testutil.PrepareBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	os.Exit(cleanup(code))
}
