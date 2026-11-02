package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestReportStates(t *testing.T) {
	now := time.Unix(100, 0)
	for state, want := range map[string]model.AgentState{
		"working": model.StateWorking, "blocked": model.StateAwaitingInput,
		"done": model.StateCompleted, "idle": "",
	} {
		b, _ := json.Marshal(Report{Name: "My agent", State: state, Message: "Check the diff"})
		a, ok := Derive(nil, "agent:My agent", b, now)
		if !ok || a.State != want || a.Provider != "agent:My agent" || a.Detail != "Check the diff" || a.UpdatedAt != now {
			t.Errorf("%s: %+v, %v", state, a, ok)
		}
		if _, ok := Derive(&a, "agent:My agent", b, now.Add(time.Second)); ok {
			t.Errorf("%s: duplicate report changes activity", state)
		}
	}
	if _, ok := Derive(nil, "agent:My agent", []byte(`{"name":"My agent"}`), now); ok {
		t.Fatal("an identity report changes activity")
	}
}

func TestDecodeReport(t *testing.T) {
	for _, input := range []string{
		`{`, `null`, `{}`, `{"name":" x"}`, `{"name":"x\n"}`,
		`{"name":"x","state":"busy"}`, `{"name":"x","message":"missing state"}`,
		`{"name":"x","state":"blocked","message":"\u001b[31m"}`,
		`{"name":"x","release":true,"state":"idle"}`,
		`{"name":"x","release":true,"resume_cmd":[]}`,
		`{"name":"x","resume_cmd":[""]}`, `{"name":"x","resume_cmd":["x","\u0000"]}`,
	} {
		if _, err := DecodeReport([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	for _, r := range []Report{
		{Name: strings.Repeat("n", 65)},
		{Name: "x", State: "working", Message: strings.Repeat("m", 1025)},
		{Name: "x", ResumeCmd: make([]string, 65)},
		{Name: "x", ResumeCmd: []string{"x", strings.Repeat("a", 8<<10)}},
	} {
		b, _ := json.Marshal(r)
		if _, err := DecodeReport(b); err == nil {
			t.Errorf("accepted oversized report")
		}
	}
	if _, err := DecodeReport(make([]byte, 16<<10+1)); err == nil {
		t.Fatal("accepted oversized payload")
	}
	for _, input := range []string{
		`{"name":"My agent","resume_cmd":["/opt/my agent","--resume","a'b"]}`,
		`{"name":"x","resume_cmd":[]}`, `{"name":"x","release":true}`,
	} {
		if _, err := DecodeReport([]byte(input)); err != nil {
			t.Errorf("%s: %v", input, err)
		}
	}
}
