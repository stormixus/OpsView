package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestDiscoverISAPIMergesVideoInputsAndStreaming(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ISAPI/System/Video/inputs/channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<VideoInputChannelList>
  <VideoInputChannel><id>1</id><inputPort>1</inputPort><name>Cam 1</name></VideoInputChannel>
  <VideoInputChannel><id>2</id><inputPort>2</inputPort><name>Cam 2</name></VideoInputChannel>
  <VideoInputChannel><id>3</id><inputPort>3</inputPort><name>Cam 3</name></VideoInputChannel>
  <VideoInputChannel><id>4</id><inputPort>4</inputPort><name>Cam 4</name></VideoInputChannel>
</VideoInputChannelList>`)
	})
	mux.HandleFunc("/ISAPI/Streaming/channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<StreamingChannelList>
  <StreamingChannel><id>101</id><channelName>Cam 1</channelName></StreamingChannel>
  <StreamingChannel><id>201</id><channelName>Cam 2</channelName></StreamingChannel>
  <StreamingChannel><id>1501</id><channelName>IPCam 15</channelName></StreamingChannel>
  <StreamingChannel><id>1601</id><channelName>IPCam 16</channelName></StreamingChannel>
</StreamingChannelList>`)
	})
	mux.HandleFunc("/ISAPI/Streaming/channels/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<StreamingChannel>
  <Video>
    <videoResolutionWidth>1920</videoResolutionWidth>
    <videoResolutionHeight>1080</videoResolutionHeight>
  </Video>
</StreamingChannel>`)
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
	expected := []int{1, 2, 3, 4, 15, 16}
	if len(channels) != len(expected) {
		t.Fatalf("expected %d channels, got %d", len(expected), len(channels))
	}
	for i, want := range expected {
		if channels[i].ChNum != want {
			t.Errorf("channel %d: expected ChNum %d, got %d", i, want, channels[i].ChNum)
		}
	}
}

func TestDiscoverISAPIFallsBackToStreamingWhenVideoInputs404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ISAPI/System/Video/inputs/channels", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
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
  <Video>
    <videoResolutionWidth>1920</videoResolutionWidth>
    <videoResolutionHeight>1080</videoResolutionHeight>
  </Video>
</StreamingChannel>`)
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
