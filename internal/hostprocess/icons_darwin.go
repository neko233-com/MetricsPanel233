//go:build darwin

package hostprocess

func nativeIcon(string) ([]byte, string, error) {
	// AppKit icon resolution requires an additional native bridge. The
	// portable binary keeps the process visible with a bundled fallback.
	return genericIcon(), "generic", nil
}
