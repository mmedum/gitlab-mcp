//go:build !unix

package localimage

// openFlags adds nothing where a FIFO cannot sit on an ordinary path.
const openFlags = 0
