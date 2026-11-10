//go:build !linux && !darwin

package daemon

import (
	"net"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Windows panes report no foreground group, so detect never reaches these.

func procSession(int) int                       { return 0 }
func procIdentify(int) (model.Provider, string) { return "", "" }
func readComm(int) string                       { return "" }
func procParent(int) (int, int)                 { return 0, 0 }
func procBusy(int) bool                         { return true }
func peerPID(net.Conn) int                      { return 0 }
