package main

import (
	"context"
	"os"
)

// App struct provides backend methods callable from the frontend.
type App struct {
	ctx context.Context
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// GetConfig returns saved relay configuration for the frontend.
func (a *App) GetConfig() map[string]string {
	url := os.Getenv("WATCH_URL")
	if url == "" {
		url = "ws://127.0.0.1:8080/watch"
	}
	token := os.Getenv("WATCH_TOKEN")
	if token == "" {
		token = "dev-watcher-token"
	}
	return map[string]string{
		"url":   url,
		"token": token,
	}
}
