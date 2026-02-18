module github.com/opsview/opsview/relay

go 1.25.6

require (
	github.com/energye/systray v1.0.2
	github.com/gorilla/websocket v1.5.3
	github.com/klauspost/compress v1.18.4
	github.com/opsview/opsview/proto v0.0.0
	golang.org/x/sys v0.29.0
)

require (
	github.com/godbus/dbus/v5 v5.0.4 // indirect
	github.com/tevino/abool v0.0.0-20220530134649-2bfc934cb23c // indirect
)

replace github.com/opsview/opsview/proto => ../proto
