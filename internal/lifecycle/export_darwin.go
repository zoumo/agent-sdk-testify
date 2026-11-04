package lifecycle

import "golang.org/x/sys/unix"

func renameExport(source, target string) error {
	// RENAME_EXCL prevents even a concurrently created empty directory from
	// being replaced; a check followed by os.Rename cannot guarantee that.
	return unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_EXCL)
}
