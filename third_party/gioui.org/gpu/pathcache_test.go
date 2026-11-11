package gpu

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/f32"
	"gioui.org/gpu/internal/driver"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

func TestPathCacheAcrossFrames(t *testing.T) {
	var frame op.Ops
	d := drawOps{pathCache: newOpCache()}
	defer d.pathCache.release()
	device := new(pathCacheDevice)
	key := func(size int, scale float32, offset int, width float32, outline bool) opKey {
		frame.Reset()
		d.reset(image.Pt(300, 300))
		defer op.Offset(image.Pt(offset, offset)).Push(&frame).Pop()
		defer op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(&frame).Pop()
		path := clip.UniformRRect(image.Rect(0, 0, size, size), 4).Path(&frame)
		if outline {
			defer clip.Outline{Path: path}.Op().Push(&frame).Pop()
		} else {
			defer clip.Stroke{Path: path, Width: width}.Op().Push(&frame).Pop()
		}
		paint.ColorOp{Color: color.NRGBA{A: 255}}.Add(&frame)
		paint.PaintOp{}.Add(&frame)
		d.collect(&frame, image.Pt(300, 300))
		d.buildPaths(device)
		if len(d.pathOps) == 0 {
			t.Fatal("no path collected")
		}
		return d.pathOps[0].pathKey
	}
	first := key(20, 1, 0, 0, true)
	if got := key(20, 1, 5, 0, true); got != first {
		t.Fatal("identical path at a new scroll offset missed the cache")
	}
	if device.uploads != 1 {
		t.Fatalf("identical path uploaded %d buffers, want 1", device.uploads)
	}
	for _, got := range []opKey{
		key(30, 1, 0, 0, true),
		key(20, 2, 0, 0, true),
		key(20, 1, 0, 1, false),
	} {
		if got == first {
			t.Error("changed geometry or stroke reused a path")
		}
	}
	if key(20, 1, 0, 1, false) == key(20, 1, 0, 2, false) {
		t.Error("different stroke widths reused a path")
	}
}

type pathCacheDevice struct {
	driver.Device
	uploads int
}

func (d *pathCacheDevice) NewImmutableBuffer(driver.BufferBinding, []byte) (driver.Buffer, error) {
	d.uploads++
	return pathCacheBuffer{}, nil
}

type pathCacheBuffer struct{ driver.Buffer }

func (pathCacheBuffer) Release() {}
