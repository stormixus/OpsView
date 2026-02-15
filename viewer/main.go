package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed frontend/*
var assets embed.FS

func main() {
	app := NewApp()
	cctv := NewCCTVManager()
	cctvProxy := NewCCTVProxyMiddleware(cctv)

	err := wails.Run(&options.App{
		Title:     "OpsView",
		Width:     1280,
		Height:    800,
		MinWidth:  800,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: cctvProxy.Middleware,
		},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			cctv.startup(ctx)
		},
		Bind: []interface{}{
			app,
			cctv,
		},
	})
	if err != nil {
		log.Fatalf("[viewer] %v", err)
	}
}
