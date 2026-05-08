package mk20

import "bytes"

// newReader is a thin wrapper around bytes.NewReader so the callsite reads
// nicely. Returning a *bytes.Reader explicitly (rather than io.Reader)
// preserves the Seek/Len interfaces in case future callers need them.
func newReader(b []byte) *bytes.Reader {
	return bytes.NewReader(b)
}
