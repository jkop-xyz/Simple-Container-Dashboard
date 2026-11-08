package web

import (
	"fmt"
	"net/http"
	"strings"
)

// sse sets up a Server-Sent Events response and calls stream with a send function.
// send writes one event (event "" is the default "message" event) and flushes it.
func sse(w http.ResponseWriter, r *http.Request, stream func(send func(event, data string) error) error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(event, data string) error {
		var b strings.Builder
		if event != "" {
			fmt.Fprintf(&b, "event: %s\n", event)
		}
		for _, line := range strings.Split(data, "\n") {
			fmt.Fprintf(&b, "data: %s\n", strings.TrimRight(line, "\r"))
		}
		b.WriteString("\n")
		if _, err := w.Write([]byte(b.String())); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	_ = stream(send) // errors mean the client went away
}
