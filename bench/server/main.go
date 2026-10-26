package main

import (
	"net/http"
	"time"
)

// Local target for pattern comparisons: /fast answers immediately, /slow after 10ms.
func main() {
	http.HandleFunc("/fast", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	http.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.Write([]byte("ok"))
	})
	http.ListenAndServe("127.0.0.1:8099", nil)
}
