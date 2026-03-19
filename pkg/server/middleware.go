package server

import (
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func requestDecompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := decodedRequestBody(r)
		if err != nil {
			serveJSONHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
		if body != r.Body {
			r.Body = body
			r.Header.Del("Content-Encoding")
			r.ContentLength = -1
		}
		next.ServeHTTP(w, r)
	})
}

func decodedRequestBody(r *http.Request) (io.ReadCloser, error) {
	encoding := strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		return r.Body, nil
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip request body: %w", err)
		}
		return &compositeReadCloser{Reader: reader, closers: []io.Closer{reader, r.Body}}, nil
	case "deflate":
		reader := flate.NewReader(r.Body)
		return &compositeReadCloser{Reader: reader, closers: []io.Closer{reader, r.Body}}, nil
	case "zstd":
		reader, err := zstd.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("invalid zstd request body: %w", err)
		}
		return &compositeReadCloser{Reader: reader, closers: []io.Closer{zstdCloser{reader}, r.Body}}, nil
	case "br":
		return nil, fmt.Errorf("unsupported content-encoding: br")
	default:
		return nil, fmt.Errorf("unsupported content-encoding: %s", encoding)
	}
}

type compositeReadCloser struct {
	io.Reader
	closers []io.Closer
}

type zstdCloser struct{ *zstd.Decoder }

func (z zstdCloser) Close() error {
	z.Decoder.Close()
	return nil
}

func (c *compositeReadCloser) Close() error {
	var firstErr error
	for _, closer := range c.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func serveJSONHTTPError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, fmt.Sprintf("{\"error\":{\"message\":%q,\"type\":\"invalid_request_error\"}}\n", msg))
}
