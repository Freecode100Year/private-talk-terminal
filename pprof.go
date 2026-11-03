//go:build pprof

package main

import (
	"net/http"
	_ "net/http/pprof"
)

// Build with -tags pprof to expose profiling on 127.0.0.1:6060 (development only).
func init() {
	go http.ListenAndServe("127.0.0.1:6060", nil)
}
