//go:build windows

package main

import (
	_ "embed"
	"log"
	"os"
	"os/exec"

	"github.com/energye/systray"
	"golang.org/x/sys/windows/registry"
)

//go:embed tray.ico
var trayIcon []byte

const (
	regPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	regKey  = "OpsViewRelay"
)

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(trayIcon)
	systray.SetTitle("OpsView Relay")
	systray.SetTooltip("OpsView Relay Server")

	stop := runServer()

	mOpen := systray.AddMenuItem("Open Web Viewer", "Open in browser")
	mOpen.Click(func() {
		exec.Command("rundll32", "url.dll,FileProtocolHandler", "http://localhost:"+getPort()).Start()
	})

	systray.AddSeparator()

	mAutoStart := systray.AddMenuItemCheckbox("Start with Windows", "Auto-start on login", isAutoStartEnabled())
	mAutoStart.Click(func() {
		toggleAutoStart(mAutoStart)
	})

	systray.AddSeparator()

	mQuit := systray.AddMenuItem("Exit", "Stop relay and exit")
	mQuit.Click(func() {
		stop()
		systray.Quit()
	})
}

func onExit() {}

func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, regPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(regKey)
	return err == nil
}

func setAutoStart(enable bool) error {
	if enable {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		k, _, err := registry.CreateKey(registry.CURRENT_USER, regPath, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		return k.SetStringValue(regKey, exe)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, regPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue(regKey)
}

func toggleAutoStart(item *systray.MenuItem) {
	enabled := isAutoStartEnabled()
	if err := setAutoStart(!enabled); err != nil {
		log.Printf("[relay] auto-start toggle error: %v", err)
		return
	}
	if enabled {
		item.Uncheck()
	} else {
		item.Check()
	}
}
