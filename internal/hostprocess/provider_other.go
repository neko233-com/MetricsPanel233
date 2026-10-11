//go:build !windows && !linux && !darwin

package hostprocess

import (
	"context"
	"errors"
)

func nativeRecords(context.Context) ([]record, string, []string, error) {
	return nil, "host", nil, errors.New("host process inventory is not supported on this operating system")
}
func nativeIcon(string) ([]byte, string, error) { return genericIcon(), "generic", nil }
