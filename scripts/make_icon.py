#!/usr/bin/env python3
import subprocess
from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[1]
SIZES = [16, 24, 32, 48, 64, 128, 256]

TILE = "#151514"
PAPER = "#f4f1ea"
SIGNAL = "#ff5a2b"
RADIUS = 7.5
GAP = 0.4
UPPER_Y = 12.75
LOWER_Y = 19.25


def svg() -> str:
    l, r = 16 - GAP, 16 + GAP
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
  <rect width="32" height="32" rx="7.5" fill="{TILE}"/>
  <path d="M{l} {UPPER_Y - RADIUS}A{RADIUS} {RADIUS} 0 0 0 {l} {UPPER_Y + RADIUS}Z" fill="{PAPER}"/>
  <path d="M{r} {LOWER_Y - RADIUS}A{RADIUS} {RADIUS} 0 0 1 {r} {LOWER_Y + RADIUS}Z" fill="{SIGNAL}"/>
</svg>
'''


def render(size: int) -> Image.Image:
    scale = 4096 / 32
    big = Image.new("RGBA", (4096, 4096), (0, 0, 0, 0))
    d = ImageDraw.Draw(big)
    d.rounded_rectangle([0, 0, 4095, 4095], radius=round(7.5 * scale), fill=TILE)

    def half(cx: float, cy: float, start: int, end: int, color: str) -> None:
        box = [(cx - RADIUS) * scale, (cy - RADIUS) * scale, (cx + RADIUS) * scale, (cy + RADIUS) * scale]
        d.pieslice(box, start, end, fill=color)

    half(16 - GAP, UPPER_Y, 90, 270, PAPER)
    half(16 + GAP, LOWER_Y, 270, 90, SIGNAL)
    return big.resize((size, size), Image.LANCZOS)


def main() -> None:
    (ROOT / "assets" / "icon.svg").write_text(svg())
    pngs = []
    for size in SIZES:
        out = Path(f"/tmp/cursor-inner-icon-{size}.png")
        render(size).save(out)
        pngs.append(str(out))
    subprocess.check_call(["convert", *pngs, str(ROOT / "assets" / "icon.ico")])
    preview = ROOT / "dist"
    preview.mkdir(exist_ok=True)
    render(256).save(preview / "icon-256.png")
    render(16).resize((128, 128), Image.NEAREST).save(preview / "icon-16-preview.png")


if __name__ == "__main__":
    main()
