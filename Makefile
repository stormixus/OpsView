.PHONY: all agent relay relay-windows viewer docker clean

all: agent relay viewer

agent:
	cd agent && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o ../build/opsview-agent.exe .

relay:
	cd relay && go build -ldflags="-s -w" -o ../build/opsview-relay .

relay-windows:
	cd relay && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui" -o ../build/opsview-relay.exe .

viewer:
	cd viewer && wails build

docker:
	docker build -t opsview-relay -f relay/Dockerfile .

clean:
	rm -rf build/
	rm -f agent/opsview-agent relay/opsview-relay
	rm -rf viewer/build/bin/
