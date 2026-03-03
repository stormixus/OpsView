.PHONY: all agent relay relay-windows viewer docker clean agent-sign

all: agent relay viewer

agent:
	cd agent && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui" -o ../build/opsview-agent.exe .

agent-sign: agent
	@mkdir -p build
	@if [ ! -f build/agent-key.pem ]; then \
		openssl req -x509 -newkey rsa:2048 -keyout build/agent-key.pem -out build/agent-cert.pem -days 3650 -nodes -subj "/CN=OpsView Agent"; \
		openssl pkcs12 -export -out build/agent.pfx -inkey build/agent-key.pem -in build/agent-cert.pem -passout pass:opsview; \
	fi
	osslsigncode sign -pkcs12 build/agent.pfx -pass opsview -n "OpsView Agent" -t http://timestamp.digicert.com -in build/opsview-agent.exe -out build/opsview-agent-signed.exe
	mv build/opsview-agent-signed.exe build/opsview-agent.exe

relay:
	cd relay && go build -ldflags="-s -w" -o ../build/opsview-relay .

relay-windows:
	cd relay && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui" -o ../build/opsview-relay.exe .

viewer:
	cd viewer && wails build

docker:
	cd relay && docker compose build

clean:
	rm -rf build/
	rm -f agent/opsview-agent relay/opsview-relay
	rm -rf viewer/build/bin/
