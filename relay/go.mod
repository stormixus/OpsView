module github.com/opsview/opsview/relay

go 1.25.6

require (
	github.com/gorilla/websocket v1.5.3
	github.com/opsview/opsview/proto v0.0.0
)

replace github.com/opsview/opsview/proto => ../proto
