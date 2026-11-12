package mobile

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"gioui.org/io/clipboard"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	_ "golang.org/x/image/webp"
)

type attachment struct {
	data   []byte
	thumb  paint.ImageOp
	remote string
}
type imageAPI interface {
	Upload(context.Context, string, []byte) (string, error)
	SendImage(context.Context, string, string, string) (uint64, error)
}

func newAttachment(b []byte) (*attachment, error) {
	if len(b) > proto.MaxPasteImage {
		return nil, errors.New("the image is over 20 MiB")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("choose a PNG, JPEG, GIF or WebP picture")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil, errors.New("the picture exceeds 40 megapixels")
	}
	im, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	// Keep only a thumbnail after decoding; the original bytes go to the daemon.
	w, h := 64, 64
	if cfg.Width > cfg.Height {
		h = max(1, 64*cfg.Height/cfg.Width)
	} else {
		w = max(1, 64*cfg.Width/cfg.Height)
	}
	thumb := image.NewNRGBA(image.Rect(0, 0, w, h))
	bounds := im.Bounds()
	for y := range h {
		for x := range w {
			thumb.Set(x, y, im.At(bounds.Min.X+x*bounds.Dx()/w, bounds.Min.Y+y*bounds.Dy()/h))
		}
	}
	return &attachment{data: b, thumb: paint.NewImageOp(thumb)}, nil
}

func (c *chatView) imageClicks(gtx C, env chatEnv) {
	k := &c.cmp
	if k.removeImage.Clicked(gtx) && !k.sending {
		k.attachment = nil
	}
	for _, b := range []struct {
		button    *widget.Clickable
		clipboard bool
	}{{&k.attach, false}, {&k.pasteImage, true}} {
		if b.button.Clicked(gtx) && env.Attach && !k.sending && !k.loadingImage {
			k.loadingImage = true
			pane := c.pane
			env.PickImage(b.clipboard, func(data []byte, err error) {
				if c.pane != pane {
					return
				}
				k.loadingImage = false
				c.setPicture(data, err)
			})
		}
	}
}

// setPicture makes data the composer's picture, or says why it is not.
// Without data or an error, as when the picker closed, nothing changes.
func (c *chatView) setPicture(data []byte, err error) {
	k := &c.cmp
	if err == nil && len(data) > 0 {
		var next *attachment
		if next, err = newAttachment(data); err == nil {
			k.attachment, k.notSent = next, ""
		}
	}
	if err != nil {
		k.notSent = err.Error()
	}
}

// paste is the field's long-press Paste: the clipboard's picture becomes the
// attachment, and text goes into the field.
func (c *chatView) paste(gtx C, env chatEnv) {
	k := &c.cmp
	if !env.Attach || k.sending || k.loadingImage {
		gtx.Execute(clipboard.ReadCmd{Tag: &k.ed})
		return
	}
	k.loadingImage = true
	pane := c.pane
	env.PickImage(true, func(data []byte, err error) {
		if c.pane != pane {
			return
		}
		k.loadingImage = false
		if errors.Is(err, errClipText) || errors.Is(err, errClipLink) {
			k.pasteText = true
			return
		}
		c.setPicture(data, err)
	})
}

func (c *chatView) attachments(gtx C, th *theme.Theme, env chatEnv) D {
	k := &c.cmp
	if !env.Attach && k.attachment == nil {
		return D{}
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			if !env.Attach {
				return D{}
			}
			semantic.LabelOp("Choose picture").Add(gtx.Ops)
			return roundButton(gtx, th, &k.attach, "paperclip", 22, th.Fg)
		}),
		layout.Rigid(func(gtx C) D {
			if !env.Attach {
				return D{}
			}
			semantic.LabelOp("Paste picture").Add(gtx.Ops)
			return roundButton(gtx, th, &k.pasteImage, "clipboard", 22, th.Fg)
		}),
		layout.Rigid(func(gtx C) D {
			if k.attachment == nil {
				return D{}
			}
			gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(48), gtx.Dp(48)))
			return widget.Image{Src: k.attachment.thumb, Fit: widget.Contain}.Layout(gtx)
		}),
		layout.Rigid(func(gtx C) D {
			if k.attachment == nil {
				return D{}
			}
			semantic.LabelOp("Remove picture").Add(gtx.Ops)
			return roundButton(gtx, th, &k.removeImage, "x", 20, th.Muted)
		}),
		layout.Rigid(func(gtx C) D {
			if !k.loadingImage && k.inserts == 0 {
				return D{}
			}
			return spinner(gtx, 20, th.Primary)
		}),
	)
}
