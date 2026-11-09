package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func fixture(args []string) error {
	if len(args) != 2 || !validWorkload(args[0]) {
		return errors.New("fixture needs a workload")
	}
	w := bufio.NewWriter(os.Stdout)
	fmt.Fprint(w, "\x1b[?25lDrawing benchmark\r\n")
	workload := args[0]
	if workload == "scroll" {
		for i := 0; i < 10000; i++ {
			fmt.Fprintf(w, "\x1b[38;5;%dmline %05d  %s\x1b[0m\r\n", 32+i%200, i, strings.Repeat("scrollback ", 8))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	rate := 250 * time.Millisecond
	if workload == "titles" {
		rate = 100 * time.Millisecond
	}
	if workload == "flood" {
		rate = time.Second / 60
		fmt.Fprint(w, "\x1b[?1049h")
	}
	tick := time.NewTicker(rate)
	defer tick.Stop()
	for n := 0; ; n++ {
		switch workload {
		case "spinners", "titles":
			fmt.Fprintf(w, "\x1b]2;%s %c\x07", args[1], []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")[n%10])
		case "flood":
			fmt.Fprint(w, "\x1b[H")
			for row := 0; row < 42; row++ {
				fmt.Fprintf(w, "\x1b[38;5;%dmCPU %02d %03d%% %s\x1b[0m\x1b[K\r\n", 32+(n+row)%200, row, (n+row)%100, strings.Repeat("▇", (n+row)%70))
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		<-tick.C
	}
}
