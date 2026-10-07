//go:build unix

package localimage

import "syscall"

// openFlags opens without waiting: a FIFO swapped in after the check
// would otherwise hold the call until something wrote to it. A regular
// file reads the same either way.
const openFlags = syscall.O_NONBLOCK
