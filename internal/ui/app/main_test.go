package app

import (
	"context"
	"errors"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/testenv"
)

// The switcher is hidden in builds; its tests run with it shown.
func TestMain(m *testing.M) {
	config.SwitcherHidden = false
	aide, conventional = config.Preset("aide"), config.Preset("conventional")
	// The side panel never reads a real session file or runs git here.
	watchFeed = func(context.Context, model.Provider, string, func(flow.Feed)) {}
	listFiles = func(context.Context, string) (string, []gitstat.FileStat, error) {
		return "", nil, errors.New("no git in tests")
	}
	testenv.Main(m)
}
