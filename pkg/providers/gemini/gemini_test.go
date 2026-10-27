package gemini

import (
	"testing"

	"github.com/lucasew/mclone/pkg/httpclient"
)

func TestHTTPClientsUseSharedPackage(t *testing.T) {
	t.Parallel()
	if listHTTPClient != httpclient.List {
		t.Fatal("listHTTPClient should be httpclient.List")
	}
	if streamHTTPClient != httpclient.Stream {
		t.Fatal("streamHTTPClient should be httpclient.Stream")
	}
}
