package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
)

//go:embed dashboard/*
var dashboardFS embed.FS

func serveDashboard(w http.ResponseWriter, r *http.Request) {
	// Serve the embedded files
	dashFS, err := fs.Sub(dashboardFS, "dashboard")
	if err != nil {
		log.Printf("Failed to sub embedded dashboard fs: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// For /dashboard, redirect to /dashboard/ to ensure relative paths work
	if r.URL.Path == "/dashboard" {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
		return
	}

	// Serve the static files under /dashboard/ prefix
	fileServer := http.StripPrefix("/dashboard/", http.FileServer(http.FS(dashFS)))
	fileServer.ServeHTTP(w, r)
}
