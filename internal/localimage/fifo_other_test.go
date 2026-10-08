//go:build !unix

package localimage

import "errors"

func mkfifo(string) error { return errors.ErrUnsupported }
