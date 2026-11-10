// Command pitwall-phone is pitwall's Android app, as a desktop window: a
// computer's tabs in the sidebar's tree and each agent tab's chat, over
// the phone page's API. scripts/apk.sh builds it for Android.
//
//	-dir path   keep the pairing here (default: the app's data folder)
//	-size WxH   window size in dp (default 412x892, a common phone)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gioui.org/app"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/mobile"
)

func main() {
	dir := flag.String("dir", "", "keep the pairing in this folder")
	size := flag.String("size", "412x892", "window size WxH in dp")
	flag.Parse()
	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil {
		log.Fatalf("-size %q: want WxH", *size)
	}
	if *dir == "" {
		d, err := app.DataDir()
		if err != nil {
			log.Fatal(err)
		}
		*dir = filepath.Join(d, "pitwall-phone")
	}
	th := mobile.Theme()
	go func() {
		win := new(app.Window)
		win.Option(append(mobile.Options(th), app.Size(unit.Dp(w), unit.Dp(h)))...)
		if err := mobile.Run(win, th, *dir); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}
