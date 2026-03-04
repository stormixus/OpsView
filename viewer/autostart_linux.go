//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

const desktopFileName = "com.opsview.viewer.desktop"

func autostartDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "autostart")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "autostart")
}

func desktopFilePath() string {
	return filepath.Join(autostartDir(), desktopFileName)
}

func isAutoStartEnabled() bool {
	_, err := os.Stat(desktopFilePath())
	return err == nil
}

func setAutoStart(enabled bool) error {
	path := desktopFilePath()
	if !enabled {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)

	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=OpsView
Exec=%s
Terminal=false
X-GNOME-Autostart-enabled=true
`, exe)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create autostart dir: %w", err)
	}
	return os.WriteFile(path, []byte(entry), 0644)
}
