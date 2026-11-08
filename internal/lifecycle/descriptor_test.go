package lifecycle

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixtureValidateRejectsPriorSchemas(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			err := (Fixture{SchemaVersion: version}).Validate()
			require.EqualError(t, err, "unsupported fixture schemaVersion "+strconv.Itoa(version))
		})
	}
}
