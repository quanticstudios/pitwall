// Command pitwall-phone is pitwall's Android app, as a desktop window: a
// computer's tabs in the sidebar's tree and each agent tab's chat, over
// the phone page's API. scripts/apk.sh builds it for Android.
//
//	-dir path          keep the pairing here (default: the app's data folder)
//	-size WxH          window size in dp (default 412x915, a common phone)
//	-demo              pair with a demo server of made-up tabs, in this process
//	-serve-demo addr   serve that demo at addr and print its pairing link, no window
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gioui.org/app"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/mobile"
)

func main() {
	dir := flag.String("dir", "", "keep the pairing in this folder")
	size := flag.String("size", "412x915", "window size WxH in dp")
	demoApp := flag.Bool("demo", false, "pair with a demo server of made-up tabs")
	demoAddr := flag.String("serve-demo", "", "serve the demo at this address and print its pairing link")
	flag.Parse()
	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil {
		log.Fatalf("-size %q: want WxH", *size)
	}
	if *demoAddr != "" {
		url, code, err := serveDemo(*demoAddr)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s#pair=%s\n", url, code)
		select {}
	}
	if *demoApp {
		url, code, err := serveDemo("127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		if *dir, err = os.MkdirTemp("", "pitwall-phone-"); err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c, err := phone.Pair(ctx, url, code, "")
		cancel()
		if err == nil {
			err = phone.Save(*dir, c)
		}
		if err != nil {
			log.Fatal(err)
		}
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
