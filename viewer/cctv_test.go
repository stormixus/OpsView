package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestDiscoverFromDVRISAPIPrefersActualChannelIDs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ISAPI/Streaming/channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<StreamingChannelList>
  <StreamingChannel><id>301</id><channelName>Channel 3</channelName></StreamingChannel>
  <StreamingChannel><id>401</id><channelName>Channel 4</channelName></StreamingChannel>
</StreamingChannelList>`)
	})
	mux.HandleFunc("/ISAPI/Streaming/channels/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<StreamingChannel>
  <videoResolutionWidth>1920</videoResolutionWidth>
  <videoResolutionHeight>1080</videoResolutionHeight>
</StreamingChannel>`)
	})
	mux.HandleFunc("/ISAPI/System/Video/inputs/channels", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/ISAPI/System/deviceInfo", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<DeviceInfo>
  <analogChannelNum>0</analogChannelNum>
  <digitalChannelNum>2</digitalChannelNum>
</DeviceInfo>`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	host, port, err := splitTestHostPort(server.Listener.Addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}

	mgr := &CCTVManager{
		client:      server.Client(),
		shortClient: server.Client(),
	}
	dvr := DVRConfig{ID: 9, Addr: host, Port: port}

	channels, err := mgr.discoverFromDVRISAPI(dvr)
	if err != nil {
		t.Fatalf("discoverFromDVRISAPI returned error: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	if channels[0].ChNum != 3 || channels[1].ChNum != 4 {
		t.Fatalf("expected channel numbers [3 4], got [%d %d]", channels[0].ChNum, channels[1].ChNum)
	}
	if channels[0].Order != 0 || channels[1].Order != 1 {
		t.Fatalf("expected normalized orders [0 1], got [%d %d]", channels[0].Order, channels[1].Order)
	}
}

func splitTestHostPort(addr net.Addr) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
