package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sahilshubham/forge/pkg"
	"github.com/sahilshubham/forge/pkg/engine/docker"
	"github.com/sahilshubham/forge/pkg/server"
	"github.com/sahilshubham/forge/pkg/store"
)

func main() {
	cfg, err := pkg.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	// Ensure data directory
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		log.Fatal(err)
	}

	// Generate SSH keypair
	keyPath, err := pkg.EnsureKeypair(cfg.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("SSH key: %s", keyPath)

	// Open store
	st, err := store.New(filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	// Create engine
	eng, err := docker.New()
	if err != nil {
		log.Fatal(err)
	}

	// Find web directory
	webDir := "web"
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		// Try relative to executable
		if ex, err := os.Executable(); err == nil {
			webDir = filepath.Join(filepath.Dir(ex), "web")
		}
	}

	// Start server
	srv := server.New(eng, st, cfg.AuthToken, webDir)
	log.Printf("forge listening on %s (web: %s)", cfg.Listen, webDir)
	if err := http.ListenAndServe(cfg.Listen, srv); err != nil {
		log.Fatal(err)
	}
}
