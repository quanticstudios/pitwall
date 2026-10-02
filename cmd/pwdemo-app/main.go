// Command pwdemo-app runs the pitwall window against a fake backend.
package main

import (
	"log"
	"os"
	"time"

	gioapp "gioui.org/app"

	"github.com/quanticstudios/pitwall/internal/ui/app"
)

func main() {
	b := app.NewFakeBackend()
	go b.Run(3*time.Second, nil)
	go func() {
		if err := app.Run(b); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	gioapp.Main()
}
