package server

import (
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/lucasew/mclone/pkg/monitor"
)

var ErrUnsupportedEncoding = errors.New("unsupported content-encoding")

func requestDecompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := decodedRequestBody(r)
		if err != nil {
			serveJSONHTTPError(w, r, http.StatusBadRequest, err.Error())
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
		return nil, fmt.Errorf("%w: br", ErrUnsupportedEncoding)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedEncoding, encoding)
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

func serveJSONHTTPError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, fmt.Sprintf("{\"error\":{\"message\":%q,\"type\":\"invalid_request_error\"}}\n", msg)); err != nil {
		monitor.ReportError(r.Context(), err, "action", "serveJSONHTTPError_write")
	}
}
