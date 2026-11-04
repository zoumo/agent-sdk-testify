//go:build !darwin && !linux

package lifecycle

import "errors"

func renameExport(_, _ string) error {
	return errors.New("atomic export without replacement requires Linux or macOS")
}
