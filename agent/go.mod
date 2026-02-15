module github.com/opsview/opsview/agent

go 1.25.6

require (
	github.com/gorilla/websocket v1.5.3
	github.com/klauspost/compress v1.18.0
	github.com/opsview/opsview/proto v0.0.0
	golang.org/x/sys v0.41.0
)

replace github.com/opsview/opsview/proto => ../proto
