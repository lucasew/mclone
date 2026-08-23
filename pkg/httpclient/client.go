// Package httpclient provides shared HTTP clients with connection-level timeouts.
// Providers reuse these so dial/header bounds stay consistent and are not copy-pasted.
package httpclient

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// statusBodyLimit is how much of a non-2xx body is included in StatusError.
const statusBodyLimit = 512

// List bounds short request/response calls (e.g. model listing).
// http.DefaultClient has no Timeout and can hang forever.
var List = &http.Client{Timeout: 30 * time.Second}

// StreamTransport bounds dial and response-header waits for long-lived streams.
// Pair with a Client that has no overall Timeout so SSE bodies can finish;
// the request context still cancels the body when the caller aborts.
var StreamTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// Stream is used for Chat SSE and other streaming APIs.
// No overall Timeout: long streams must be able to complete.
var Stream = &http.Client{Transport: StreamTransport}

// StatusError returns nil when resp is 2xx.
// Otherwise it reads up to 512 bytes of the body and wraps sentinel with the
// status code and optional trimmed body text. readContext labels a body-read failure.
func StatusError(resp *http.Response, sentinel error, readContext string) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, statusBodyLimit))
	if err != nil {
		return fmt.Errorf("%s: status %d: read body: %w", readContext, resp.StatusCode, err)
	}
	msg := strings.TrimSpace(string(body))
	if msg != "" {
		return fmt.Errorf("%w %d: %s", sentinel, resp.StatusCode, msg)
	}
	return fmt.Errorf("%w %d", sentinel, resp.StatusCode)
}
