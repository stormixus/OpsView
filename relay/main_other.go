//go:build !windows

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	stop := runServer()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("[relay] shutting down...")
	stop()
}
