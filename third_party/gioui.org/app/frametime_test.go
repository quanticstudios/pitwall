package app

import (
	"image"
	"image/color"
	"slices"
	"testing"

	"gioui.org/gpu"
	"gioui.org/op"
)

func TestFrameSubmittedBeforePresentAndAck(t *testing.T) {
	r := &timingRenderer{}
	w := Window{ctx: r, gpu: r}
	ack := make(chan struct{}, 1)
	w.FrameSubmitted = func() {
		if len(ack) != 0 {
			t.Fatal("acknowledged before submission callback")
		}
		r.events = append(r.events, "submitted")
	}
	r.present = func() {
		if len(ack) != 1 {
			t.Fatal("presentation started before acknowledgement")
		}
	}
	if err := w.validateAndProcess(image.Pt(10, 10), false, new(op.Ops), ack); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.events, []string{"frame", "submitted", "present"}) {
		t.Fatal(r.events)
	}
}

type timingRenderer struct {
	events  []string
	present func()
}

func (r *timingRenderer) Frame(*op.Ops, gpu.RenderTarget, image.Point) error {
	r.events = append(r.events, "frame")
	return nil
}
func (r *timingRenderer) Present() error {
	r.events = append(r.events, "present")
	r.present()
	return nil
}
func (*timingRenderer) Clear(color.NRGBA)                       {}
func (*timingRenderer) Release()                                {}
func (*timingRenderer) API() gpu.API                            { return gpu.OpenGL{} }
func (*timingRenderer) RenderTarget() (gpu.RenderTarget, error) { return gpu.OpenGLRenderTarget{}, nil }
func (*timingRenderer) Refresh() error                          { return nil }
func (*timingRenderer) Lock() error                             { return nil }
func (*timingRenderer) Unlock()                                 {}
