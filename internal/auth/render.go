package auth

import (
	"bytes"
	"net/http"
)

// bufferedResponseWriter stages a rendered page so status and body can be
// committed only after template execution succeeds.
type bufferedResponseWriter struct {
	header http.Header
	body   bytes.Buffer
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (w *bufferedResponseWriter) Header() http.Header { return w.header }

func (w *bufferedResponseWriter) WriteHeader(int) {}

func (w *bufferedResponseWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func writeBufferedResponse(dst http.ResponseWriter, status int, src *bufferedResponseWriter) {
	for key, values := range src.header {
		if http.CanonicalHeaderKey(key) == "Set-Cookie" {
			for _, value := range values {
				dst.Header().Add(key, value)
			}
			continue
		}
		dst.Header()[key] = append([]string(nil), values...)
	}
	dst.WriteHeader(status)
	_, _ = dst.Write(src.body.Bytes())
}
