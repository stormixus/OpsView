package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed frontend/*
var assets embed.FS

func main() {
	app := NewApp()
	cctv := NewCCTVManager()
	stream := NewStreamProxy()
	updater := NewUpdater()
	proxy := NewAssetProxyMiddleware(cctv, stream)

	err := wails.Run(&options.App{
		Title:     "OpsView",
		Width:     1280,
		Height:    800,
		MinWidth:  800,
		MinHeight: 600,
		Frameless: false,
		Mac: &mac.Options{
			TitleBar:            mac.TitleBarHidden(),
			WindowIsTranslucent: false,
			About: &mac.AboutInfo{
				Title:   "OpsView",
				Message: "Remote Monitoring & Control",
			},
		},
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: proxy.Middleware,
		},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			cctv.startup(ctx)
			updater.startup(ctx)
		},
		OnShutdown: func(ctx context.Context) {
			cctv.Shutdown()
			stream.StopStream()
		},
		Bind: []interface{}{
			app,
			cctv,
			stream,
			updater,
		},
	})
	if err != nil {
		log.Fatalf("[viewer] %v", err)
	}
}
