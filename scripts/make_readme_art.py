#!/usr/bin/env python3
"""生成 README 用的横幅、原理图（SVG）和带窗口外框的截图（PNG），各有深色、浅色两版。

截图外框以 docs/images/web-{zh,en}-{dark,light}.png 为输入，需要 Pillow。
"""
from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "docs" / "images"

SANS = "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue', Arial, 'PingFang SC', 'Microsoft YaHei', sans-serif"
MONO = "ui-monospace, 'Cascadia Mono', 'JetBrains Mono', SFMono-Regular, Menlo, Consolas, monospace"

THEMES = {
    "dark": {
        "bg": "#0e0e0d", "panel": "#161615", "raise": "#1d1d1b", "line": "#2a2a27",
        "text": "#f4f1ea", "muted": "#a19d94", "faint": "#6a675f",
        "signal": "#ff5a2b", "signal_soft": "#3a1d13", "tile": "#151514", "tile_line": "#2f2f2c",
    },
    "light": {
        "bg": "#f6f5f1", "panel": "#ffffff", "raise": "#f3f2ee", "line": "#e2e0da",
        "text": "#151514", "muted": "#67645e", "faint": "#9a968e",
        "signal": "#e94a1c", "signal_soft": "#fde8e0", "tile": "#151514", "tile_line": "#151514",
    },
}

MARK = (
    '<path d="M15.6 5.25A7.5 7.5 0 0 0 15.6 20.25Z" fill="#f4f1ea"/>'
    '<path d="M16.4 11.75A7.5 7.5 0 0 1 16.4 26.75Z" fill="#ff5a2b"/>'
)


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def banner(t: dict) -> str:
    rows = [
        ("Claude Opus", "Cursor", False),
        ("GPT-5", "Cursor", False),
        ("DeepSeek V4 Flash", "cursor-inner", True),
        ("Qwen3 Coder", "cursor-inner", False),
    ]
    items = []
    y = 132
    for name, source, active in rows:
        if active:
            items.append(f'<rect x="744" y="{y - 30}" width="424" height="48" rx="10" fill="{t["signal_soft"]}"/>')
            items.append(f'<path d="M766 {y - 7} l6 6 l11 -12" fill="none" stroke="{t["signal"]}" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>')
        ours = source == "cursor-inner"
        items.append(f'<text x="798" y="{y}" font-family="{SANS}" font-size="19" font-weight="{600 if active else 500}" fill="{t["text"]}">{esc(name)}</text>')
        width = 112 if ours else 74
        x = 1150 - width
        stroke = t["signal"] if ours else t["line"]
        fill = t["signal"] if ours and active else "none"
        color = "#ffffff" if ours and active else (t["signal"] if ours else t["muted"])
        items.append(f'<rect x="{x}" y="{y - 21}" width="{width}" height="28" rx="14" fill="{fill}" stroke="{stroke}" stroke-width="1.5"/>')
        items.append(f'<text x="{x + width / 2}" y="{y - 2}" text-anchor="middle" font-family="{MONO}" font-size="13" fill="{color}">{source}</text>')
        y += 58
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 400" width="1280" height="400">
  <defs>
    <radialGradient id="glow" cx="0.74" cy="0.55" r="0.5">
      <stop offset="0" stop-color="{t["signal"]}" stop-opacity="0.16"/>
      <stop offset="1" stop-color="{t["signal"]}" stop-opacity="0"/>
    </radialGradient>
  </defs>
  <rect width="1280" height="400" rx="24" fill="{t["bg"]}"/>
  <rect width="1280" height="400" rx="24" fill="url(#glow)"/>
  <g transform="translate(88 64) scale(4)">
    <rect width="32" height="32" rx="7.5" fill="{t["tile"]}" stroke="{t["tile_line"]}" stroke-width="0.25"/>
    {MARK}
  </g>
  <text x="86" y="268" font-family="{SANS}" font-size="60" font-weight="700" letter-spacing="-1.5" fill="{t["text"]}">cursor-inner</text>
  <text x="88" y="312" font-family="{SANS}" font-size="23" fill="{t["muted"]}">Bring your own models into Cursor.</text>
  <text x="88" y="346" font-family="{SANS}" font-size="19" fill="{t["faint"]}">在 Cursor 里接入自己的模型，官方模型照常使用</text>
  <rect x="720" y="56" width="472" height="288" rx="18" fill="{t["panel"]}" stroke="{t["line"]}" stroke-width="1.5"/>
  <text x="746" y="88" font-family="{MONO}" font-size="12" letter-spacing="2" fill="{t["faint"]}">MODELS</text>
  {"".join(items)}
</svg>
'''


FLOW_TEXT = {
    "zh": {
        "cursor": "Cursor", "cursor_sub": "Windows · macOS · Linux",
        "inner_sub": "127.0.0.1 本机代理", "inner_note": "只解密 *.cursor.sh",
        "api": "你的模型接口", "api_sub": "OpenAI / Anthropic 兼容",
        "official": "Cursor 官方服务", "official_sub": "官方模型 · 模型目录",
        "other": "其他主机", "other_sub": "原样隧道，不解密",
        "e_proxy": "HTTP 代理", "e_custom": "选中自定义模型", "e_official": "官方模型，原样转发", "e_other": "非 *.cursor.sh",
    },
    "en": {
        "cursor": "Cursor", "cursor_sub": "Windows · macOS · Linux",
        "inner_sub": "local proxy on 127.0.0.1", "inner_note": "decrypts *.cursor.sh only",
        "api": "Your model endpoint", "api_sub": "OpenAI / Anthropic compatible",
        "official": "Cursor servers", "official_sub": "official models · catalog",
        "other": "Other hosts", "other_sub": "tunneled, not decrypted",
        "e_proxy": "HTTP proxy", "e_custom": "custom model selected", "e_official": "official model, unchanged", "e_other": "not *.cursor.sh",
    },
}


def flow(t: dict, s: dict) -> str:
    def node(x, y, w, h, title, sub, stroke, dash=""):
        return (
            f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="14" fill="{t["panel"]}" stroke="{stroke}" stroke-width="1.6"{dash}/>'
            f'<text x="{x + w / 2}" y="{y + h / 2 - 4}" text-anchor="middle" font-family="{SANS}" font-size="19" font-weight="600" fill="{t["text"]}">{esc(title)}</text>'
            f'<text x="{x + w / 2}" y="{y + h / 2 + 20}" text-anchor="middle" font-family="{SANS}" font-size="13.5" fill="{t["muted"]}">{esc(sub)}</text>'
        )

    def label(x, y, text, color):
        return (f'<text x="{x}" y="{y}" text-anchor="middle" font-family="{SANS}" font-size="13.5" fill="{color}" '
                f'stroke="{t["bg"]}" stroke-width="6" paint-order="stroke" stroke-linejoin="round">{esc(text)}</text>')

    dash = ' stroke-dasharray="6 6"'
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1200 420" width="1200" height="420">
  <defs>
    <marker id="a-signal" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L10 5L0 10z" fill="{t["signal"]}"/></marker>
    <marker id="a-text" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L10 5L0 10z" fill="{t["muted"]}"/></marker>
    <marker id="a-faint" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L10 5L0 10z" fill="{t["faint"]}"/></marker>
  </defs>
  <rect width="1200" height="420" rx="24" fill="{t["bg"]}"/>
  <path d="M260 210 H408" stroke="{t["muted"]}" stroke-width="2" fill="none" marker-end="url(#a-text)"/>
  <path d="M690 180 C780 180 790 82 880 82" stroke="{t["signal"]}" stroke-width="2.6" fill="none" marker-end="url(#a-signal)"/>
  <path d="M690 210 H880" stroke="{t["muted"]}" stroke-width="2" fill="none" marker-end="url(#a-text)"/>
  <path d="M690 240 C780 240 790 338 880 338" stroke="{t["faint"]}" stroke-width="2" fill="none"{dash} marker-end="url(#a-faint)"/>
  {label(334, 198, s["e_proxy"], t["muted"])}
  {label(785, 118, s["e_custom"], t["signal"])}
  {label(785, 198, s["e_official"], t["muted"])}
  {label(785, 316, s["e_other"], t["faint"])}
  {node(60, 170, 200, 80, s["cursor"], s["cursor_sub"], t["line"])}
  <rect x="410" y="140" width="280" height="140" rx="18" fill="{t["panel"]}" stroke="{t["signal"]}" stroke-width="2"/>
  <g transform="translate(430 160) scale(1.25)"><rect width="32" height="32" rx="7.5" fill="{t["tile"]}"/>{MARK}</g>
  <text x="480" y="187" font-family="{SANS}" font-size="22" font-weight="700" fill="{t["text"]}">cursor-inner</text>
  <text x="430" y="232" font-family="{SANS}" font-size="14" fill="{t["muted"]}">{esc(s["inner_sub"])}</text>
  <text x="430" y="256" font-family="{MONO}" font-size="13" fill="{t["signal"]}">{esc(s["inner_note"])}</text>
  {node(882, 42, 260, 80, s["api"], s["api_sub"], t["signal"])}
  {node(882, 170, 260, 80, s["official"], s["official_sub"], t["line"])}
  {node(882, 298, 260, 80, s["other"], s["other_sub"], t["line"], dash)}
</svg>
'''


def window(lang: str, theme: str) -> None:
    shot = Image.open(OUT / f"web-{lang}-{theme}.png").convert("RGBA")
    t = THEMES[theme]
    bar = 56
    radius = 22
    pad = 72
    w, h = shot.size[0], shot.size[1] + bar
    frame = Image.new("RGBA", (w, h), t["panel"])
    draw = ImageDraw.Draw(frame)
    for i, color in enumerate(("#ff5f57", "#febc2e", "#28c840")):
        cx = 30 + i * 30
        draw.ellipse([cx - 8, bar / 2 - 8, cx + 8, bar / 2 + 8], fill=color)
    draw.line([(0, bar - 1), (w, bar - 1)], fill=t["line"], width=2)
    frame.paste(shot, (0, bar))

    mask = Image.new("L", (w, h), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, w - 1, h - 1], radius=radius, fill=255)
    border = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    ImageDraw.Draw(border).rounded_rectangle([0, 0, w - 1, h - 1], radius=radius, outline=t["line"], width=2)

    canvas = Image.new("RGBA", (w + pad * 2, h + pad * 2), (0, 0, 0, 0))
    shadow = Image.new("RGBA", canvas.size, (0, 0, 0, 0))
    alpha = 110 if theme == "dark" else 60
    ImageDraw.Draw(shadow).rounded_rectangle([pad, pad + 18, pad + w, pad + h + 18], radius=radius, fill=(0, 0, 0, alpha))
    canvas = Image.alpha_composite(canvas, shadow.filter(ImageFilter.GaussianBlur(28)))
    canvas.paste(frame, (pad, pad), mask)
    canvas = Image.alpha_composite(canvas, _offset(border, canvas.size, pad))
    canvas.save(OUT / f"window-{lang}-{theme}.png", optimize=True)


def _offset(layer: Image.Image, size, pad: int) -> Image.Image:
    out = Image.new("RGBA", size, (0, 0, 0, 0))
    out.paste(layer, (pad, pad), layer)
    return out


def main() -> None:
    for theme, t in THEMES.items():
        (OUT / f"banner-{theme}.svg").write_text(banner(t), encoding="utf-8")
        for lang, s in FLOW_TEXT.items():
            (OUT / f"flow-{lang}-{theme}.svg").write_text(flow(t, s), encoding="utf-8")
            window(lang, theme)


if __name__ == "__main__":
    main()
