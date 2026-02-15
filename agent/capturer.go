package main

import "github.com/opsview/opsview/proto"

// Capturer is the interface for screen capture backends.
// On Windows, DXGICapturer implements this using Desktop Duplication API.
// On other platforms, a dummy capturer is used for development/testing.
type Capturer interface {
	// CaptureFrame captures the current screen and returns changed tiles.
	// Returns tiles, screen width, screen height, and error.
	CaptureFrame() ([]proto.Tile, int, int, error)

	// Close releases capture resources.
	Close()
}
