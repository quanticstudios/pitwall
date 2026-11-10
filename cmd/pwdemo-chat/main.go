// Command pwdemo-chat shows the phone app's chat screen with demo data, at
// a phone's size, for visual checks against the design.
//
//	-state name  the state to show (default following); -list prints them
//	-shot file   write a PNG of the state with no window, and exit
//	-scale n     px per dp for -shot (default 2)
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"gioui.org/app"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/mobile"
)

func main() {
	state := flag.String("state", "following", "the state to show")
	shot := flag.String("shot", "", "write a PNG of the state here and exit")
	scale := flag.Float64("scale", 2, "px per dp for -shot")
	list := flag.Bool("list", false, "print the states and exit")
	flag.Parse()
	if *list {
		fmt.Println(strings.Join(mobile.DemoStates, "\n"))
		return
	}
	th := mobile.Theme()
	if *shot != "" {
		if err := mobile.ChatShot(th, *state, *shot, float32(*scale)); err != nil {
			log.Fatal(err)
		}
		return
	}
	go func() {
		w := new(app.Window)
		w.Option(append(mobile.Options(th), app.Size(unit.Dp(412), unit.Dp(860)))...)
		if err := mobile.ChatDemo(w, th, *state); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}
