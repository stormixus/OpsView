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
	github.com/NebulousLabs/go-upnp v0.0.0-20181203152547-b32978b8ccbf // indirect
	github.com/godbus/dbus/v5 v5.0.4 // indirect
	github.com/tevino/abool v0.0.0-20220530134649-2bfc934cb23c // indirect
	gitlab.com/NebulousLabs/fastrand v0.0.0-20181126182046-603482d69e40 // indirect
	gitlab.com/NebulousLabs/go-upnp v0.0.0-20211002182029-11da932010b6 // indirect
	golang.org/x/crypto v0.0.0-20210322153248-0c34fe9e7dc2 // indirect
	golang.org/x/net v0.0.0-20210410081132-afb366fc7cd1 // indirect
	golang.org/x/text v0.3.6 // indirect
)

replace github.com/opsview/opsview/proto => ../proto
