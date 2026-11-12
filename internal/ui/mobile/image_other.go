//go:build !android

package mobile

import (
	"errors"

	"gioui.org/app"
)

const canPickImage = false

func setImageView(app.ViewEvent, func(int), func(int, []byte, error)) {}
func pickImage(clipboard bool, done func([]byte, error)) {
	done(nil, errors.New("photo picking requires Android"))
}

func openUpdateURL(string) error { return errors.New("opening an APK requires Android") }
