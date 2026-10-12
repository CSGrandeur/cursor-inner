package program

import "math"

// 状态图标 = 程序图标 + 右下角一个圆点：警告是琥珀色，更新中是蓝色。
// 圆点外圈挖掉一道透明缝，深色、浅色任务栏上都分得清。像素在 Go 里合成，Windows 侧只负责取图和建图标。

type rgb struct{ r, g, b uint8 }

var (
	badgeWarn   = rgb{0xF5, 0xA5, 0x24}
	badgeUpdate = rgb{0x3B, 0x8E, 0xF0}
)

func badgeColor(level trayLevel) (rgb, bool) {
	switch level {
	case trayWarn:
		return badgeWarn, true
	case trayUpdating:
		return badgeUpdate, true
	}
	return rgb{}, false
}

// badgePixels 在 size×size 的 BGRA（直通 alpha，自上而下）像素上画徽标，返回新切片，不改原图。
// 4×4 超采样抗锯齿。
func badgePixels(src []byte, size int, c rgb) []byte {
	out := append([]byte(nil), src...)
	if size <= 0 || len(src) < size*size*4 {
		return out
	}
	s := float64(size)
	r := math.Max(3, s*0.28)     // 圆点半径
	gap := math.Max(1, s/16)     // 透明缝宽
	cx, cy := s-r-s/32, s-r-s/32 // 贴近右下角
	const n = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var dot, cut float64
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					px := float64(x) + (float64(sx)+0.5)/n
					py := float64(y) + (float64(sy)+0.5)/n
					d := math.Hypot(px-cx, py-cy)
					if d <= r {
						dot++
					} else if d <= r+gap {
						cut++
					}
				}
			}
			if dot == 0 && cut == 0 {
				continue
			}
			dot /= n * n
			cut /= n * n
			i := (y*size + x) * 4
			// 先挖缝：原图 alpha 按覆盖比例减掉
			a := float64(out[i+3]) / 255 * (1 - cut - dot)
			// 再把圆点盖上去（直通 alpha 的 over 合成）
			na := dot + a
			if na <= 0 {
				out[i], out[i+1], out[i+2], out[i+3] = 0, 0, 0, 0
				continue
			}
			mix := func(base byte, top uint8) byte {
				return byte(math.Round((float64(top)*dot + float64(base)*a) / na))
			}
			out[i] = mix(out[i], c.b)
			out[i+1] = mix(out[i+1], c.g)
			out[i+2] = mix(out[i+2], c.r)
			out[i+3] = byte(math.Round(na * 255))
		}
	}
	return out
}
