//go:build windows

package program

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap       = gdi32.NewProc("CreateBitmap")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procDrawIconEx         = user32.NewProc("DrawIconEx")
	procCreateIconIndirect = user32.NewProc("CreateIconIndirect")
)

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

// dibSection 建一个 size×size、32 位、自上而下的位图，返回句柄和像素切片。
func dibSection(size int) (uintptr, []byte) {
	bi := bitmapInfoHeader{size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), width: int32(size), height: -int32(size), planes: 1, bitCount: 32}
	var bits unsafe.Pointer
	h, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if h == 0 || bits == nil {
		return 0, nil
	}
	return h, unsafe.Slice((*byte)(bits), size*size*4)
}

// iconPixels 取出图标的 BGRA 像素（直通 alpha）。分别画在黑底和白底上反推 alpha，
// 带 alpha 通道的图标和只有掩码的老图标都适用。
func iconPixels(icon uintptr, size int) ([]byte, bool) {
	draw := func(bg byte) []byte {
		dc, _, _ := procCreateCompatibleDC.Call(0)
		if dc == 0 {
			return nil
		}
		defer procDeleteDC.Call(dc)
		bmp, px := dibSection(size)
		if bmp == 0 {
			return nil
		}
		defer procDeleteObject.Call(bmp)
		old, _, _ := procSelectObject.Call(dc, bmp)
		for i := range px {
			px[i] = bg
		}
		const diNormal = 0x3
		r, _, _ := procDrawIconEx.Call(dc, 0, 0, icon, uintptr(size), uintptr(size), 0, 0, diNormal)
		_, _, _ = procSelectObject.Call(dc, old)
		if r == 0 {
			return nil
		}
		return append([]byte(nil), px...)
	}
	black, white := draw(0), draw(255)
	if black == nil || white == nil {
		return nil, false
	}
	out := make([]byte, len(black))
	for i := 0; i < len(out); i += 4 {
		a := 255 - (int(white[i+1]) - int(black[i+1]))
		if a <= 0 {
			continue
		}
		if a > 255 {
			a = 255
		}
		for c := 0; c < 3; c++ {
			v := int(black[i+c]) * 255 / a
			if v > 255 {
				v = 255
			}
			out[i+c] = byte(v)
		}
		out[i+3] = byte(a)
	}
	return out, true
}

// iconFromPixels 用 BGRA（直通 alpha）像素建图标。调用方负责 DestroyIcon（这里缓存到进程结束）。
func iconFromPixels(px []byte, size int) uintptr {
	color, bits := dibSection(size)
	if color == 0 {
		return 0
	}
	defer procDeleteObject.Call(color)
	copy(bits, px)
	stride := (size + 15) / 16 * 2 // 单色位图每行按 16 位对齐
	mask := make([]byte, stride*size)
	hmask, _, _ := procCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, uintptr(unsafe.Pointer(&mask[0])))
	if hmask == 0 {
		return 0
	}
	defer procDeleteObject.Call(hmask)
	ii := iconInfo{fIcon: 1, hbmMask: hmask, hbmColor: color}
	icon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return icon
}
