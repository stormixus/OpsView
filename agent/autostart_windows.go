//go:build windows

package main

import (
	"log"
	"os"

	"golang.org/x/sys/windows/registry"
)

const autoStartKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const autoStartValueName = "OpsViewAgent"

func setAutoStart(enable bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, autoStartKeyPath, registry.SET_VALUE)
	if err != nil {
		log.Printf("[autostart] open key: %v", err)
		return
	}
	defer key.Close()

	if enable {
		exe, err := os.Executable()
		if err != nil {
			log.Printf("[autostart] get exe path: %v", err)
			return
		}
		if err := key.SetStringValue(autoStartValueName, exe); err != nil {
			log.Printf("[autostart] set value: %v", err)
			return
		}
		log.Println("[autostart] enabled")
	} else {
		if err := key.DeleteValue(autoStartValueName); err != nil {
			log.Printf("[autostart] delete value: %v", err)
			return
		}
		log.Println("[autostart] disabled")
	}
}
