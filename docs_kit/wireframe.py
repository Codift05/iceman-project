# -*- coding: utf-8 -*-
"""Renderer wireframe low fidelity untuk dokumen Iceman.

Semua elemen digambar siku-siku, monokrom, dan memakai Liberation Sans (metrik
sama dengan Arial) agar menyatu dengan dokumen. Dipakai oleh 06 UI/UX Spec.

    s = Screen("Keranjang")
    s.appbar("Keranjang", back=True)
    s.listitem("Es Kristal Balok 10 kg", "Rp 25.000 / balok", right="x2")
    s.button("Lanjut ke Pengiriman")
    sheet([s1, s2], "out.png")
"""
import os
from PIL import Image, ImageDraw, ImageFont

F_REG = "/usr/share/fonts/liberation-sans-fonts/LiberationSans-Regular.ttf"
F_BOLD = "/usr/share/fonts/liberation-sans-fonts/LiberationSans-Bold.ttf"

S = 3                       # faktor render (piksel = logis x S)
INK = (0, 0, 0)
MUTED = (110, 110, 110)
LINE = (60, 60, 60)
HAIR = (185, 185, 185)
FILL_SOFT = (242, 242, 242)
FILL_DARK = (60, 60, 60)
WHITE = (255, 255, 255)

PAD = 14                    # margin dalam layar


def _f(size, bold=False):
    return ImageFont.truetype(F_BOLD if bold else F_REG, int(size * S))


class Screen:
    """Satu layar wireframe dengan tata letak mengalir dari atas ke bawah."""

    def __init__(self, name, w=360, h=720, kind="mobile"):
        self.name, self.kind = name, kind
        self.w, self.h = w, h
        self.im = Image.new("RGB", (w * S, h * S), WHITE)
        self.d = ImageDraw.Draw(self.im)
        self.y = 0
        self.x0 = PAD                 # margin kiri area konten
        self._has_tabbar = False
        self.body = 12 if kind == "mobile" else 15
        self.small = 10 if kind == "mobile" else 12
        self.d.rectangle([0, 0, w * S - 1, h * S - 1], outline=LINE, width=max(1, S // 2))

    # ---------- primitif ----------
    def _rect(self, x0, y0, x1, y1, fill=None, outline=LINE, width=1):
        self.d.rectangle([x0 * S, y0 * S, x1 * S, y1 * S],
                         fill=fill, outline=outline, width=max(1, int(width * S / 2)))

    def _text(self, x, y, txt, size=None, bold=False, color=INK, anchor="la"):
        size = size or self.body
        self.d.text((x * S, y * S), txt, font=_f(size, bold), fill=color, anchor=anchor)

    def _tw(self, txt, size=None, bold=False):
        size = size or self.body
        return self.d.textlength(txt, font=_f(size, bold)) / S

    def _clip(self, txt, maxw, size=None, bold=False):
        if self._tw(txt, size, bold) <= maxw:
            return txt
        while txt and self._tw(txt + "...", size, bold) > maxw:
            txt = txt[:-1]
        return txt + "..."

    def gap(self, n=8):
        self.y += n
        return self

    # ---------- komponen ----------
    def appbar(self, title, back=False, action=None):
        h = 40 if self.kind == "mobile" else 46
        y0 = self.y
        self._rect(0, y0, self.w, y0 + h, fill=FILL_SOFT)
        x = self.x0
        if back:
            cy = y0 + h / 2
            self.d.line([(x + 6) * S, (cy - 5) * S, (x + 1) * S, cy * S], fill=INK, width=S)
            self.d.line([(x + 1) * S, cy * S, (x + 6) * S, (cy + 5) * S], fill=INK, width=S)
            x += 16
        self._text(x, y0 + h / 2, title, size=self.body + 3, bold=True, anchor="lm")
        if action:
            self._text(self.w - PAD, y0 + h / 2, action, size=self.small,
                       anchor="rm", color=MUTED)
        self.y = y0 + h + 10
        return self

    def statusbar(self, left="Iceman", right=""):
        self._rect(0, 0, self.w, 16, fill=FILL_DARK, outline=FILL_DARK)
        self._text(self.x0, 8, left, size=self.small - 1, color=WHITE, anchor="lm")
        if right:
            self._text(self.w - PAD, 8, right, size=self.small - 1, color=WHITE, anchor="rm")
        self.y = 16
        return self

    def title(self, txt, sub=None):
        self._text(self.x0, self.y, txt, size=self.body + 4, bold=True)
        self.y += self.body + 9
        if sub:
            self._text(self.x0, self.y, sub, size=self.small, color=MUTED)
            self.y += self.small + 7
        return self.gap(4)

    def text(self, txt, bold=False, muted=False, size=None):
        size = size or self.body
        self._text(self.x0, self.y, self._clip(txt, self.w - 2 * PAD, size, bold),
                   size=size, bold=bold, color=MUTED if muted else INK)
        self.y += size + 6
        return self

    def field(self, label, value="", hint=False):
        self._text(self.x0, self.y, label, size=self.small, color=MUTED)
        self.y += self.small + 4
        h = 26 if self.kind == "mobile" else 30
        self._rect(self.x0, self.y, self.w - PAD, self.y + h, fill=WHITE)
        self._text(PAD + 8, self.y + h / 2, value, size=self.body,
                   color=MUTED if hint else INK, anchor="lm")
        self.y += h + 10
        return self

    def button(self, label, primary=True, half=False):
        h = 32 if self.kind == "mobile" else 34
        x1 = (self.w - PAD) if not half else ((self.x0 + self.w) / 2 - 4)
        self._rect(self.x0, self.y, x1, self.y + h,
                   fill=FILL_DARK if primary else WHITE)
        self._text((PAD + x1) / 2, self.y + h / 2, label, size=self.body, bold=True,
                   color=WHITE if primary else INK, anchor="mm")
        if not half:
            self.y += h + 10
        return self

    def button_row(self, left, right):
        h = 32
        mid = (self.x0 + self.w) / 2
        self._rect(self.x0, self.y, mid - 4, self.y + h, fill=WHITE)
        self._text((PAD + mid - 4) / 2, self.y + h / 2, left, size=self.body,
                   bold=True, anchor="mm")
        self._rect(mid + 4, self.y, self.w - PAD, self.y + h, fill=FILL_DARK)
        self._text((mid + 4 + self.w - PAD) / 2, self.y + h / 2, right, size=self.body,
                   bold=True, color=WHITE, anchor="mm")
        self.y += h + 10
        return self

    def divider(self, strong=False):
        self.d.line([self.x0 * S, self.y * S, (self.w - PAD) * S, self.y * S],
                    fill=LINE if strong else HAIR, width=max(1, S // 2))
        self.y += 10
        return self

    def kv(self, label, value, bold=False):
        self._text(self.x0, self.y, label, size=self.body, bold=bold)
        self._text(self.w - PAD, self.y, value, size=self.body, bold=bold, anchor="ra")
        self.y += self.body + 7
        return self

    def listitem(self, title, sub=None, right=None, thumb=False, badge=None):
        h = 44 if sub else 30
        x = self.x0
        if thumb:
            self._rect(self.x0, self.y, self.x0 + 34, self.y + 34, fill=FILL_SOFT)
            self.d.line([self.x0 * S, self.y * S, (self.x0 + 34) * S, (self.y + 34) * S],
                        fill=HAIR, width=max(1, S // 2))
            self.d.line([(self.x0 + 34) * S, self.y * S, PAD * S, (self.y + 34) * S],
                        fill=HAIR, width=max(1, S // 2))
            x = self.x0 + 42
            h = max(h, 40)
        rw = self._tw(right, self.small) + 10 if right else 0
        self._text(x, self.y + 1, self._clip(title, self.w - x - PAD - rw, self.body, True),
                   size=self.body, bold=True)
        if right:
            self._text(self.w - PAD, self.y + 2, right, size=self.small, anchor="ra")
        if sub:
            self._text(x, self.y + self.body + 6,
                       self._clip(sub, self.w - x - PAD - rw, self.small), size=self.small,
                       color=MUTED)
        if badge:
            self.badge(badge, x=x, y=self.y + h - 12)
        self.y += h + 6
        self.d.line([self.x0 * S, (self.y - 4) * S, (self.w - PAD) * S, (self.y - 4) * S],
                    fill=HAIR, width=max(1, S // 2))
        self.y += 4
        return self

    def badge(self, txt, x=None, y=None):
        x = PAD if x is None else x
        y = self.y if y is None else y
        w = self._tw(txt, self.small - 1, True) + 12
        self._rect(x, y, x + w, y + 14, fill=FILL_SOFT)
        self._text(x + 6, y + 7, txt, size=self.small - 1, bold=True, anchor="lm")
        return w

    def status_row(self, items, active=0):
        """Rangkaian status order/pengiriman."""
        n = len(items)
        cw = (self.w - self.x0 - PAD) / n
        for i, it in enumerate(items):
            cx = self.x0 + cw * i + cw / 2
            r = 5
            fill = FILL_DARK if i <= active else WHITE
            self._rect(cx - r, self.y, cx + r, self.y + 2 * r, fill=fill)
            if i < n - 1:
                self.d.line([(cx + r) * S, (self.y + r) * S, (cx + cw - r) * S, (self.y + r) * S],
                            fill=LINE if i < active else HAIR, width=max(1, S // 2))
            self._text(cx, self.y + 2 * r + 5, self._clip(it, cw - 2, self.small - 1),
                       size=self.small - 1, anchor="ma",
                       color=INK if i <= active else MUTED)
        self.y += 2 * r + self.small + 12
        return self

    def chips(self, items, active=0):
        x = self.x0
        for i, it in enumerate(items):
            w = self._tw(it, self.small) + 16
            if x + w > self.w - PAD:
                break
            self._rect(x, self.y, x + w, self.y + 20,
                       fill=FILL_DARK if i == active else WHITE)
            self._text(x + w / 2, self.y + 10, it, size=self.small,
                       color=WHITE if i == active else INK, anchor="mm")
            x += w + 6
        self.y += 28
        return self

    def checkbox(self, label, checked=False):
        b = 12
        self._rect(self.x0, self.y, self.x0 + b, self.y + b, fill=FILL_DARK if checked else WHITE)
        self._text(self.x0 + b + 8, self.y + b / 2, label, size=self.body, anchor="lm")
        self.y += b + 10
        return self

    def stepper(self, qty="1", label=None, price=None):
        b = 22
        x = self.x0
        if label:
            self._text(self.x0, self.y + b / 2, self._clip(label, self.w - 150), anchor="lm")
        x = self.w - PAD - (b * 3 + 16)
        self._rect(x, self.y, x + b, self.y + b)
        self._text(x + b / 2, self.y + b / 2, "-", bold=True, anchor="mm")
        self._rect(x + b + 4, self.y, x + 2 * b + 12, self.y + b, fill=WHITE)
        self._text(x + b + 4 + (b + 8) / 2, self.y + b / 2, qty, anchor="mm")
        self._rect(x + 2 * b + 16, self.y, x + 3 * b + 16, self.y + b)
        self._text(x + 2 * b + 16 + b / 2, self.y + b / 2, "+", bold=True, anchor="mm")
        self.y += b + 10
        return self

    def imagebox(self, h=110, label="foto produk"):
        self._rect(self.x0, self.y, self.w - PAD, self.y + h, fill=FILL_SOFT)
        self.d.line([self.x0 * S, self.y * S, (self.w - PAD) * S, (self.y + h) * S],
                    fill=HAIR, width=max(1, S // 2))
        self.d.line([(self.w - PAD) * S, self.y * S, PAD * S, (self.y + h) * S],
                    fill=HAIR, width=max(1, S // 2))
        self._text(self.w / 2, self.y + h / 2, label, size=self.small, color=MUTED, anchor="mm")
        self.y += h + 10
        return self

    def grid(self, cells, cols=2, ch=54):
        cw = (self.w - self.x0 - PAD - (cols - 1) * 8) / cols
        for i, (t, v) in enumerate(cells):
            r, c = divmod(i, cols)
            x = self.x0 + c * (cw + 8)
            y = self.y + r * (ch + 8)
            self._rect(x, y, x + cw, y + ch, fill=WHITE)
            self._text(x + 8, y + 9, self._clip(t, cw - 16, self.small), size=self.small,
                       color=MUTED)
            self._text(x + 8, y + 9 + self.small + 5, self._clip(v, cw - 16, self.body + 3, True),
                       size=self.body + 3, bold=True)
        rows = (len(cells) + cols - 1) // cols
        self.y += rows * (ch + 8) + 4
        return self

    def table(self, header, rows, widths=None):
        n = len(header)
        tw = self.w - self.x0 - PAD
        widths = widths or [tw / n] * n
        widths = [w * tw / sum(widths) for w in widths]
        rh = 22 if self.kind == "mobile" else 26
        x = self.x0
        self._rect(self.x0, self.y, self.w - PAD, self.y + rh, fill=FILL_SOFT)
        for i, hcol in enumerate(header):
            self._text(x + 6, self.y + rh / 2, self._clip(hcol, widths[i] - 10, self.small, True),
                       size=self.small, bold=True, anchor="lm")
            x += widths[i]
        self.y += rh
        for row in rows:
            x = self.x0
            self._rect(self.x0, self.y, self.w - PAD, self.y + rh, fill=WHITE, outline=HAIR)
            for i, cell in enumerate(row[:n]):
                self._text(x + 6, self.y + rh / 2, self._clip(str(cell), widths[i] - 10, self.small),
                           size=self.small, anchor="lm")
                x += widths[i]
            self.y += rh
        self.y += 10
        return self

    def tabbar(self, items, active=0):
        self._has_tabbar = True
        h = 44
        y0 = self.h - h
        self._rect(0, y0, self.w, self.h, fill=FILL_SOFT)
        cw = self.w / len(items)
        for i, it in enumerate(items):
            cx = cw * i + cw / 2
            self._rect(cx - 8, y0 + 9, cx + 8, y0 + 21,
                       fill=FILL_DARK if i == active else WHITE)
            self._text(cx, y0 + 28, it, size=self.small - 1, bold=(i == active),
                       anchor="ma", color=INK if i == active else MUTED)
        return self

    def sidebar(self, items, active=0, width=150):
        self._rect(0, 0, width, self.h, fill=FILL_SOFT)
        self._text(self.x0, 24, "ICEMAN ADMIN", size=self.small + 1, bold=True, anchor="lm")
        y = 52
        for i, it in enumerate(items):
            if i == active:
                self._rect(0, y - 4, width, y + 20, fill=FILL_DARK, outline=FILL_DARK)
            self._text(self.x0, y + 8, it, size=self.small + 1,
                       color=WHITE if i == active else INK, anchor="lm",
                       bold=(i == active))
            y += 26
        self.content_x = width
        self.x0 = width + 18
        return self

    def topbar(self, title, right="Admin Ops"):
        x0 = getattr(self, "content_x", 0)
        self._rect(x0, 0, self.w, 44, fill=WHITE)
        self._text(x0 + 18, 22, title, size=self.body + 3, bold=True, anchor="lm")
        self._text(self.w - 18, 22, right, size=self.small, color=MUTED, anchor="rm")
        self.y = 58
        self._shift = x0 + 18
        return self

    def note(self, txt):
        """Catatan anotasi di bawah layar."""
        self._text(self.x0, self.y, txt, size=self.small - 1, color=MUTED)
        self.y += self.small + 4
        return self

    def crop(self, min_h=280):
        """Pangkas tinggi layar sampai sebatas isi, lalu gambar ulang bingkainya."""
        if self._has_tabbar:
            return self.im
        h = int(max(min_h, min(self.h, self.y + PAD)))
        im = self.im.crop((0, 0, self.w * S, h * S))
        d = ImageDraw.Draw(im)
        d.rectangle([0, 0, self.w * S - 1, h * S - 1], outline=LINE, width=max(1, S // 2))
        self.im, self.h = im, h
        return im

    def image(self):
        return self.im


def sheet(screens, out_path, labels=None, cols=2, gap=26, label_h=26):
    """Susun beberapa layar berdampingan menjadi satu gambar."""
    labels = labels or [s.name for s in screens]
    for s in screens:
        s.crop()
    rows = (len(screens) + cols - 1) // cols
    sw = max(s.w for s in screens)
    sh = max(s.h for s in screens)
    W = cols * sw + (cols - 1) * gap
    H = rows * (sh + label_h) + (rows - 1) * gap
    out = Image.new("RGB", (W * S, H * S), WHITE)
    d = ImageDraw.Draw(out)
    for i, s in enumerate(screens):
        r, c = divmod(i, cols)
        x = c * (sw + gap)
        y = r * (sh + label_h + gap)
        out.paste(s.im, (int(x * S), int(y * S)))
        d.text(((x + sw / 2) * S, (y + sh + 7) * S), labels[i],
               font=_f(11, True), fill=INK, anchor="ma")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    out.save(out_path, "PNG", optimize=True)
    return out_path
