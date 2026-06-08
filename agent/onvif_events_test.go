package main

import "testing"

const sampleGetEventPropertiesResp = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
 <s:Body>
  <tev:GetEventPropertiesResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl"
       xmlns:tns1="http://www.onvif.org/ver10/topics">
   <wstop:TopicSet xmlns:wstop="http://docs.oasis-open.org/wsn/t-1">
    <tns1:VideoSource><MotionAlarm wstop:topic="true"/></tns1:VideoSource>
    <tns1:RuleEngine><CellMotionDetector><Motion wstop:topic="true"/></CellMotionDetector></tns1:RuleEngine>
   </wstop:TopicSet>
  </tev:GetEventPropertiesResponse>
 </s:Body>
</s:Envelope>`

func TestParseEventTopics(t *testing.T) {
	topics := parseEventTopics([]byte(sampleGetEventPropertiesResp))
	if len(topics) == 0 {
		t.Fatal("expected topics, got none")
	}
	joined := ""
	for _, tp := range topics {
		joined += tp + "\n"
	}
	for _, want := range []string{"MotionAlarm", "CellMotionDetector"} {
		if !containsSub(joined, want) {
			t.Fatalf("topics %q missing %q", joined, want)
		}
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
