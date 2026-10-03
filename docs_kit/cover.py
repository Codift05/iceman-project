# -*- coding: utf-8 -*-
"""Generator halaman cover Iceman.

Memakai artwork cover asli (motif es + logo) sebagai latar, lalu menimpa blok
judul dengan teks dokumen yang diminta. Logo dan motif es tidak pernah disentuh.
"""
import os
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
BASE = os.path.join(HERE, "assets", "cover_base.png")
F_BOLD = "/usr/share/fonts/liberation-sans-fonts/LiberationSans-Bold.ttf"
F_REG = "/usr/share/fonts/liberation-sans-fonts/LiberationSans-Regular.ttf"

SCALE = 3                      # 736x1041 -> 2208x3123 (setara ~267 dpi di A4)
INK = (17, 17, 17)

# Area aman untuk ditimpa, dalam koordinat sumber 736x1041.
# Dipilih agar tidak menyentuh tetesan logo (x>=520, y<310) dan motif es bawah (y>=731).
WIPE = [(40, 294, 500, 726), (495, 515, 530, 726)]

LEFT = 74                      # margin kiri blok judul
# (garis_dasar, ukuran_font, tebal, lebar_maks) dalam koordinat sumber 736x1041
LAYOUT = {
    "code":     (416, 150, True, 430),
    "title":    (459, 31, False, 460),
    "product":  (502, 25, False, 460),
    "subtitle": (548, 19, True, 460),
    "meta":     (576, 16, False, 460),
}
META_LEADING = 24


def _font(path, size):
    return ImageFont.truetype(path, size)


def _fit(draw, text, path, size, max_w):
    """Perkecil font sampai teks muat pada lebar maksimum."""
    f = _font(path, size)
    while size > 8:
        if draw.textlength(text, font=f) <= max_w:
            return f
        size -= 2
        f = _font(path, size)
    return f


def make_cover(code, title, product, subtitle, meta_lines, out_path):
    im = Image.open(BASE).convert("RGB")
    if SCALE != 1:
        im = im.resize((im.width * SCALE, im.height * SCALE), Image.LANCZOS)
    d = ImageDraw.Draw(im)

    for x0, y0, x1, y1 in WIPE:
        d.rectangle([x0 * SCALE, y0 * SCALE, x1 * SCALE, y1 * SCALE], fill=(255, 255, 255))

    x = LEFT * SCALE

    def put(key, text, bold=None):
        y, size, b, maxw = LAYOUT[key]
        path = F_BOLD if (b if bold is None else bold) else F_REG
        f = _fit(d, text, path, size * SCALE, maxw * SCALE)
        d.text((x, y * SCALE), text, font=f, fill=INK, anchor="ls")

    put("code", code)
    put("title", title)
    put("product", product)
    put("subtitle", subtitle)

    y, size, b, maxw = LAYOUT["meta"]
    for i, line in enumerate(meta_lines):
        ff = _fit(d, line, F_REG, size * SCALE, maxw * SCALE)
        d.text((x, (y + i * META_LEADING) * SCALE), line, font=ff, fill=INK, anchor="ls")

    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    im.save(out_path, "PNG", optimize=True)
    return out_path


if __name__ == "__main__":
    out = make_cover(
        code="PRD",
        title="Product Requirements Document",
        product="ICEMAN APPS",
        subtitle="Aplikasi Pemesanan dan Distribusi Es Kristal",
        meta_lines=["Versi 1.1  |  Draft untuk Validasi & Discovery",
                    "Disusun berdasarkan dokumen",
                    "“Spesifikasi Kebutuhan Iceman Apps”"],
        out_path=os.path.join(HERE, "build", "cover_prd.png"))
    print("cover dibuat:", out)
