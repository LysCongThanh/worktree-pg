//go:build spike

// Spike S7: does net.Listen detect ports published by docker (docker-proxy or iptables-only)?
// Probes ":p", "127.0.0.1:p", "localhost:p" and "[::1]:p" for each port given on argv.
//
// Usage: go run -tags spike ./scripts/spikes/s7_listen 15415 15421
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

// try reports whether a TCP listener can be opened on addr (true = free).
func try(addr string) string {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return "busy"
	}
	_ = l.Close()
	return "free"
}

func main() {
	for _, a := range os.Args[1:] {
		p, _ := strconv.Atoi(a)
		fmt.Printf("port %d: any=%s v4loop=%s localhost=%s v6loop=%s\n", p,
			try(fmt.Sprintf(":%d", p)), try(fmt.Sprintf("127.0.0.1:%d", p)),
			try(fmt.Sprintf("localhost:%d", p)), try(fmt.Sprintf("[::1]:%d", p)))
	}
}
