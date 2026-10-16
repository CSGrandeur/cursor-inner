package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"strings"
	"testing"
)

func noisyPNG(t *testing.T, w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFitImageShrinksLargeImage(t *testing.T) {
	data := noisyPNG(t, 2400, 1200)
	out, mime, err := fitImage(data, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if max(cfg.Width, cfg.Height) > imageMaxEdge || len(out) >= len(data) || mime == "" {
		t.Fatalf("%dx%d %d bytes (was %d) %s", cfg.Width, cfg.Height, len(out), len(data), mime)
	}
	if cfg.Width < cfg.Height {
		t.Fatal("aspect ratio lost")
	}
}

func TestFitImageKeepsSmallImage(t *testing.T) {
	data := noisyPNG(t, 40, 30)
	out, mime, err := fitImage(data, "image/png")
	if err != nil || !bytes.Equal(out, data) || mime != "image/png" {
		t.Fatalf("err=%v changed=%v mime=%s", err, !bytes.Equal(out, data), mime)
	}
}

func TestReadImagePlaceholderOnFailure(t *testing.T) {
	bad := append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, imageTargetBytes+1)...)
	text, images := readImage("Read image file: a.webp", bad, "image/webp")
	if len(images) != 0 || !strings.Contains(text, imageUnreadable) {
		t.Fatalf("%q %d", text, len(images))
	}
}

func TestFitImageKeepsSmallWebP(t *testing.T) {
	data := append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 100)...)
	out, mime, err := fitImage(data, "image/webp")
	if err != nil || !bytes.Equal(out, data) || mime != "image/webp" {
		t.Fatalf("err=%v mime=%s", err, mime)
	}
}
