package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func parseReport(args []string) (agent.Report, error) {
	var r agent.Report
	var clear bool
	f := flag.NewFlagSet("report-agent", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&r.Name, "name", "", "agent name or label")
	f.StringVar(&r.State, "state", "", "working, blocked, done or idle")
	f.StringVar(&r.Message, "message", "", "short status message")
	f.BoolVar(&r.Release, "release", false, "clear the agent on exit")
	f.BoolVar(&clear, "no-resume", false, "clear the resume command")
	if err := f.Parse(args); err != nil {
		return r, err
	}
	if len(f.Args()) > 0 {
		if clear {
			return r, errors.New("--no-resume cannot include a resume command")
		}
		// The command follows -- so its flags belong to the agent.
		r.ResumeCmd = f.Args()
	} else if clear {
		r.ResumeCmd = []string{}
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	return agent.DecodeReport(payload)
}

func runReportAgent(args []string) error {
	r, err := parseReport(args)
	if err != nil {
		return err
	}
	pane := os.Getenv("PITWALL_PANE")
	if pane == "" {
		return nil
	}
	path := os.Getenv("PITWALL_SOCKET")
	if path == "" {
		if path, err = proto.SocketPath(); err != nil {
			return nil
		}
	}
	payload, _ := json.Marshal(r)
	sendHook(path, pane, model.Provider("report"), payload)
	return nil
}
