package app

import (
	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
)

// aide is the preset the nav, tab-mode and window tests were written for.
var aide = config.Preset("aide")

// tabPrefix is the aide preset's tab prefix key, held with Ctrl.
const tabPrefix key.Name = "T"
