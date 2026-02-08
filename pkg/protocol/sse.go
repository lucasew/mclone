package protocol

import (
	"bytes"
	"fmt"
	"net/http"
	"sync"

	"github.com/goccy/go-json"
)

var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func WriteSSE[T interface{}](w http.ResponseWriter, event string, data T) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteString("\ndata: ")
	json.NewEncoder(buf).Encode(data)
	// json.Encoder adds a \n, SSE needs \n\n
	// We rely on the encoder's newline and just add one more
	buf.WriteString("\n")

	w.Write(buf.Bytes())
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func WriteSSEData[T interface{}](w http.ResponseWriter, data T) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	buf.WriteString("data: ")
	json.NewEncoder(buf).Encode(data)
	buf.WriteString("\n")

	w.Write(buf.Bytes())
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func WriteSSERaw(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func SetStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

func WriteJSON[T interface{}](w http.ResponseWriter, data T) {
	json.NewEncoder(w).Encode(data)
}
