//go:build !android

package mobile

import (
	"errors"

	"gioui.org/app"
)

const canPickImage = false

func setImageView(app.ViewEvent, func([]byte, error)) {}
func pickImage(clipboard bool, done func([]byte, error)) {
	done(nil, errors.New("photo picking requires Android"))
}
