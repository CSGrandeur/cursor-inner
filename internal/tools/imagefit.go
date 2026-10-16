package tools

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"cursor-inner/internal/provider"
)

// 读入图片时就缩小，避免原图随每一轮请求反复发送。
// 长边 1568px 取自 hermes-agent tools/vision_tools.py（_EMBED_MAX_DIMENSION，与 Anthropic 的标准分辨率一致），
// 约 256KB 的目标同样来自那里；原图已在范围内时原样保留（与 Codex 的做法一致）。
const (
	imageMaxEdge     = 1568
	imageTargetBytes = 256 * 1024
	imageMinEdge     = 512
)

// imageUnreadable 是图片无法缩小时留给模型的说明，原图不再发送。
const imageUnreadable = "[图片过大且无法压缩，已省略]"

var errImageFit = errors.New("image cannot be reduced")

// fitImage 把图片缩到长边不超过 imageMaxEdge、体积约 imageTargetBytes。
// 原图已在范围内时原样返回；无法解码（例如 WebP）又超出范围时返回错误。
func fitImage(data []byte, mime string) ([]byte, string, error) {
	cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(data))
	if cfgErr == nil && max(cfg.Width, cfg.Height) <= imageMaxEdge && len(data) <= imageTargetBytes {
		return data, mime, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// 标准库解不了 WebP：体积在目标内就原样保留，否则只留说明。
		if mime == "image/webp" && len(data) <= imageTargetBytes {
			return data, mime, nil
		}
		return nil, "", errImageFit
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, "", errImageFit
	}
	edge := min(max(w, h), imageMaxEdge)
	var best []byte
	for {
		img := scaleTo(src, edge)
		if out, ok := encodeSmallest(img); ok {
			best = out.data
			if len(out.data) <= imageTargetBytes {
				return out.data, out.mime, nil
			}
		}
		if edge <= imageMinEdge {
			break
		}
		edge = max(edge*4/5, imageMinEdge)
	}
	if best != nil && len(best) < len(data) {
		return best, "image/jpeg", nil
	}
	return nil, "", errImageFit
}

type encoded struct {
	data []byte
	mime string
}

// encodeSmallest 先试 PNG（文字截图更清楚），超出目标再用 JPEG。
func encodeSmallest(img *image.NRGBA) (encoded, bool) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err == nil && buf.Len() <= imageTargetBytes {
		return encoded{buf.Bytes(), "image/png"}, true
	}
	var last encoded
	for _, q := range []int{85, 70} {
		buf.Reset()
		if err := jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: q}); err != nil {
			return encoded{}, false
		}
		last = encoded{append([]byte(nil), buf.Bytes()...), "image/jpeg"}
		if buf.Len() <= imageTargetBytes {
			return last, true
		}
	}
	return last, true
}

// flatten 把透明部分铺上白底，JPEG 不支持透明。
func flatten(img *image.NRGBA) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			a := uint32(c.A)
			blend := func(v uint8) uint8 { return uint8((uint32(v)*a + 255*(255-a)) / 255) }
			out.SetRGBA(x, y, color.RGBA{blend(c.R), blend(c.G), blend(c.B), 255})
		}
	}
	return out
}

// scaleTo 按面积平均把图片缩到长边为 edge（不放大）。
func scaleTo(src image.Image, edge int) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if max(w, h) > edge {
		if w >= h {
			nw, nh = edge, max(h*edge/w, 1)
		} else {
			nw, nh = max(w*edge/h, 1), edge
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0 := b.Min.Y + y*h/nh
		y1 := max(b.Min.Y+(y+1)*h/nh, y0+1)
		for x := 0; x < nw; x++ {
			x0 := b.Min.X + x*w/nw
			x1 := max(b.Min.X+(x+1)*w/nw, x0+1)
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					c := color.NRGBAModel.Convert(src.At(xx, yy)).(color.NRGBA)
					r += uint64(c.R)
					g += uint64(c.G)
					bl += uint64(c.B)
					a += uint64(c.A)
					n++
				}
			}
			out.SetNRGBA(x, y, color.NRGBA{uint8(r / n), uint8(g / n), uint8(bl / n), uint8(a / n)})
		}
	}
	return out
}

// readImage 是两个读图入口共用的处理：缩小成功就附图，失败就只留说明文字。
func readImage(label string, data []byte, mime string) (string, []provider.Image) {
	out, outMIME, err := fitImage(data, mime)
	if err != nil {
		return label + "\n" + imageUnreadable, nil
	}
	return label, []provider.Image{{MIME: outMIME, Data: append([]byte(nil), out...)}}
}
