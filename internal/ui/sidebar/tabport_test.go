package sidebar

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// A tab's row shows its port block's first open port, else the lowest
// port a server it started listens on.
func TestTabPort(t *testing.T) {
	st := &model.State{
		Listening: map[string][]int{"a": {3010, 3012}},
		Servers: map[string][]model.Server{
			"a": {{PID: 1, Ports: []int{80}}},
			"b": {{PID: 2, Ports: []int{8000}}, {PID: 3, Ports: []int{5173, 9000}}, {PID: 4}},
		},
	}
	for _, tc := range []struct {
		ws    string
		port  int
		block bool
	}{{"a", 3010, true}, {"b", 5173, false}, {"c", 0, false}} {
		if port, block := tabPort(st, tc.ws); port != tc.port || block != tc.block {
			t.Errorf("tabPort(%s) = %d %v, want %d %v", tc.ws, port, block, tc.port, tc.block)
		}
	}
}
