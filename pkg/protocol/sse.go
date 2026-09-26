package protocol

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/monitor"
)

var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func WriteSSE[T any](w http.ResponseWriter, event string, data T) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteString("\ndata: ")
	if err := json.NewEncoder(buf).Encode(data); err != nil {
		monitor.ReportError(context.Background(), err, "action", "WriteSSE_Encode")
		return
	}
	// json.Encoder adds a \n, SSE needs \n\n
	// We rely on the encoder's newline and just add one more
	buf.WriteString("\n")

	if _, err := w.Write(buf.Bytes()); err != nil {
		monitor.ReportError(context.Background(), err, "action", "WriteSSE_Write")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func WriteSSEData[T any](w http.ResponseWriter, data T) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	buf.WriteString("data: ")
	if err := json.NewEncoder(buf).Encode(data); err != nil {
		monitor.ReportError(context.Background(), err, "action", "WriteSSEData_Encode")
		return
	}
	buf.WriteString("\n")

	if _, err := w.Write(buf.Bytes()); err != nil {
		monitor.ReportError(context.Background(), err, "action", "WriteSSEData_Write")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func WriteSSERaw(w http.ResponseWriter, data string) {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		monitor.ReportError(context.Background(), err, "action", "WriteSSERaw_Write")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func SetStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

func WriteJSON[T any](w http.ResponseWriter, data T) {
	if err := json.NewEncoder(w).Encode(data); err != nil {
		monitor.ReportError(context.Background(), err, "action", "WriteJSON_Encode")
	}
}
