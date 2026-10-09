package tools

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// imageMIME 认出 Cursor 读文件返回的 PNG、JPEG、GIF、WebP。
// 认不出或解不出尺寸时返回空字符串，调用方按普通二进制文件处理。
func imageMIME(data []byte) string {
	if len(data) < 12 {
		return ""
	}
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		if !imageConfigOK(data) {
			return ""
		}
		return "image/png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		if !imageConfigOK(data) {
			return ""
		}
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		if !imageConfigOK(data) {
			return ""
		}
		return "image/gif"
	case bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		if !webpOK(data) {
			return ""
		}
		return "image/webp"
	default:
		return ""
	}
}

func imageConfigOK(data []byte) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && cfg.Width > 0 && cfg.Height > 0
}

func webpOK(data []byte) bool {
	if len(data) < 20 || binary.LittleEndian.Uint32(data[4:8]) < 12 {
		return false
	}
	i := 12
	for i+8 <= len(data) {
		kind := string(data[i : i+4])
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		if size < 0 {
			return false
		}
		start := i + 8
		if start > len(data) {
			return false
		}
		end := start + size
		if end > len(data) {
			end = len(data)
		}
		switch kind {
		case "VP8X":
			return vp8xOK(data[start:end])
		case "VP8 ":
			return vp8OK(data[start:end])
		case "VP8L":
			return vp8lOK(data[start:end])
		}
		i = start + size
		if size%2 == 1 {
			i++
		}
	}
	return false
}

func vp8xOK(data []byte) bool {
	if len(data) < 10 {
		return false
	}
	width := int(data[4]) | int(data[5])<<8 | int(data[6])<<16
	height := int(data[7]) | int(data[8])<<8 | int(data[9])<<16
	return width+1 > 0 && height+1 > 0
}

func vp8OK(data []byte) bool {
	if len(data) < 10 || data[0]&1 != 0 {
		return false
	}
	if data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
		return false
	}
	width := int(binary.LittleEndian.Uint16(data[6:8])) & 0x3fff
	height := int(binary.LittleEndian.Uint16(data[8:10])) & 0x3fff
	return width > 0 && height > 0
}

func vp8lOK(data []byte) bool {
	if len(data) < 5 || data[0] != 0x2f {
		return false
	}
	bits := binary.LittleEndian.Uint32(data[1:5])
	width := int(bits&0x3fff) + 1
	height := int((bits>>14)&0x3fff) + 1
	return width > 0 && height > 0
}
