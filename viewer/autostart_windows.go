//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const (
	regPath    = `Software\Microsoft\Windows\CurrentVersion\Run`
	regKeyName = "OpsView"
)

func isAutoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, regPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()

	_, _, err = k.GetStringValue(regKeyName)
	return err == nil
}

func setAutoStart(enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, regPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("failed to open registry key: %w", err)
	}
	defer k.Close()

	if !enabled {
		err := k.DeleteValue(regKeyName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	return k.SetStringValue(regKeyName, exe)
}
