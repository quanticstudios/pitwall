//go:build !android

package mobile

import "gioui.org/app"

// canScan reports whether the pairing screen offers Scan QR code. Only
// the Android build has a camera behind it.
const canScan = false

func setCameraView(app.ViewEvent)           {}
func startCamera(func(frame), func(string)) {}
func stopCamera()                           {}
