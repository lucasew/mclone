// Package httpclient provides shared HTTP clients with connection-level timeouts.
// Providers reuse these so dial/header bounds stay consistent and are not copy-pasted.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

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
