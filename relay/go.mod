module github.com/opsview/opsview/relay

go 1.25.6

require (
	github.com/NebulousLabs/go-upnp v0.0.0-20181203152547-b32978b8ccbf
	github.com/bluenviron/gohlslib/v2 v2.2.5
	github.com/bluenviron/gortsplib/v5 v5.3.1
	github.com/energye/systray v1.0.2
	github.com/gorilla/websocket v1.5.3
	github.com/klauspost/compress v1.18.4
	github.com/opsview/opsview/proto v0.0.0
	github.com/pion/rtp v1.10.1
	golang.org/x/sys v0.40.0
)

require (
	github.com/abema/go-mp4 v1.4.1 // indirect
	github.com/asticode/go-astikit v0.30.0 // indirect
	github.com/asticode/go-astits v1.14.0 // indirect
	github.com/bluenviron/mediacommon/v2 v2.7.1 // indirect
	github.com/godbus/dbus/v5 v5.0.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/sdp/v3 v3.0.17 // indirect
	github.com/pion/srtp/v3 v3.0.10 // indirect
	github.com/pion/transport/v4 v4.0.1 // indirect
	github.com/tevino/abool v0.0.0-20220530134649-2bfc934cb23c // indirect
	gitlab.com/NebulousLabs/fastrand v0.0.0-20181126182046-603482d69e40 // indirect
	gitlab.com/NebulousLabs/go-upnp v0.0.0-20211002182029-11da932010b6 // indirect
	golang.org/x/crypto v0.47.0 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/text v0.33.0 // indirect
)

replace github.com/opsview/opsview/proto => ../proto
