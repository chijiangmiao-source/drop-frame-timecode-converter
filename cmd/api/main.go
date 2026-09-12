package main

import (
	"log"
	"os"

	"dropframe-api/internal/httpapi"
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("dropframe-api listening on %s", addr)
	if err := httpapi.NewRouter().Run(addr); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
