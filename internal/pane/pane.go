// Package pane runs one process in a PTY and feeds its output to a vt
// emulator.
package pane

import "github.com/quanticstudios/pitwall/internal/vt"

type Config struct {
	ID         string
	Cmd        []string // empty means $SHELL
	Cwd        string
	Env        []string // added on top of os.Environ(); Start also sets PITWALL_PANE, TERM, COLORTERM
	Cols, Rows int
	NewVT      vt.NewFunc
}

type Pane struct{}

func Start(c Config) (*Pane, error) { panic("unimplemented") }

// Write sends input bytes to the process.
func (p *Pane) Write(b []byte) (int, error) { panic("unimplemented") }
func (p *Pane) Resize(cols, rows int) error { panic("unimplemented") }
func (p *Pane) Snapshot() vt.Grid           { panic("unimplemented") }
func (p *Pane) Modes() vt.Modes             { panic("unimplemented") }

// Dirty has capacity 1 and is signalled after output lands in the emulator.
func (p *Pane) Dirty() <-chan struct{} { panic("unimplemented") }
func (p *Pane) Done() <-chan struct{}  { panic("unimplemented") }
func (p *Pane) ExitCode() int          { panic("unimplemented") }

// Cwd is the live working directory of the foreground process.
func (p *Pane) Cwd() string { panic("unimplemented") }

// Close sends SIGHUP and kills the process group after 2s.
func (p *Pane) Close() error { panic("unimplemented") }
