package utils

import "io"

// CountingReadCloser wraps an io.ReadCloser and calls a callback function with the number of bytes read on each Read call.
type CountingReadCloser struct {
	io.ReadCloser
	OnRead func(int)
}

func (c *CountingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if c.OnRead != nil && n > 0 {
		c.OnRead(n)
	}
	return n, err
}
