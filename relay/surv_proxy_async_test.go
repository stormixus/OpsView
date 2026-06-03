package main

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/opsview/opsview/proto"
)

// HandleSurvConfig now runs off the publisher read loop, so it may be invoked
// concurrently. Run with -race: configMu must serialize applications.
func TestHandleSurvConfigConcurrentSafe(t *testing.T) {
	sp := NewSurvProxy()
	defer sp.StopAll()

	payload, _ := json.Marshal(proto.SurvConfig{}) // empty: no RTSP dials

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sp.HandleSurvConfig(payload)
		}()
	}
	wg.Wait()
}
