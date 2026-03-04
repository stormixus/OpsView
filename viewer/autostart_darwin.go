//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const plistLabel = "com.opsview.viewer"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
}

func isAutoStartEnabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

func setAutoStart(enabled bool) error {
	path := plistPath()
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

	// If inside a .app bundle, use "open -a /path/to.app"
	var programArgs string
	if idx := strings.Index(exe, ".app/"); idx != -1 {
		appPath := exe[:idx+4]
		programArgs = fmt.Sprintf(`    <array>
        <string>/usr/bin/open</string>
        <string>-a</string>
        <string>%s</string>
    </array>`, appPath)
	} else {
		programArgs = fmt.Sprintf(`    <array>
        <string>%s</string>
    </array>`, exe)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
%s
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
</dict>
</plist>
`, plistLabel, programArgs)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents dir: %w", err)
	}
	return os.WriteFile(path, []byte(plist), 0644)
}
