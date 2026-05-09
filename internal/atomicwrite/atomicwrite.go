// Package atomicwrite provides a shared atomic-write helper for ash verbs.
//
// Write replaces a file via temp-file + rename on the same directory, so a
// mid-write crash leaves the original file intact. If temp-file creation or
// rename fails the error is returned — there is no silent fallback to a direct
// write that would sacrifice the atomicity guarantee.
package atomicwrite

import (
	"os"
	"path/filepath"
)

// Options configures optional behavior for Write.
type Options struct {
	// PreserveMode copies the file mode of the existing target to the
	// replacement. Has no effect if the target does not yet exist.
	PreserveMode bool

	// TmpPrefix is used as the temp-file name prefix (e.g. ".ash-write-").
	// If empty, ".ash-write-" is used.
	TmpPrefix string
}

// Write atomically writes data to path by creating a temp file in the same
// directory and renaming it into place. The caller is responsible for ensuring
// the parent directory exists before calling Write.
func Write(path string, data []byte, opts Options) error {
	prefix := opts.TmpPrefix
	if prefix == "" {
		prefix = ".ash-write-"
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	if opts.PreserveMode {
		if info, err := os.Stat(path); err == nil {
			os.Chmod(tmpName, info.Mode())
		}
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
