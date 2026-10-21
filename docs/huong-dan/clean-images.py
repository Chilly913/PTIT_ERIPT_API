"""Tidy the guide screenshots in place: paint over the Next.js dev badge (round "N" button, bottom-left) with the page
background and trim the empty band under the content. Safe to run again.

    PTIT_ERIPT_AI/.venv/Scripts/python PTIT_ERIPT_API/docs/huong-dan/clean-images.py
"""
import sys
from pathlib import Path

from PIL import Image, ImageChops

IMG = Path(__file__).resolve().parent / "img"
PAD = 28  # keep a little breathing room under the last content row


def clean(path: Path) -> bool:
    im = Image.open(path).convert("RGB")
    w, h = im.size
    # the dev badge sits ~20px from the left edge and ~20px from the bottom of the viewport
    bg_side = im.getpixel((6, max(0, h - 8)))
    x0, y0, x1, y1 = 8, h - 66, 76, h - 4
    if y0 > 0:
        im.paste(bg_side, (x0, y0, x1, y1))
    # main page background: sample the bottom-right corner, which is always empty page
    bg = im.getpixel((w - 4, h - 4))
    diff = ImageChops.difference(im, Image.new("RGB", im.size, bg)).convert("L").point(lambda v: 255 if v > 12 else 0)
    # ignore the sidebar column (it is a different colour all the way down): look at the content area only
    content = diff.crop((230, 0, w, h))
    box = content.getbbox()
    if not box:
        return False
    bottom = min(h, box[3] + PAD)
    if bottom >= h - 4:
        im.save(path)
        return False
    im.crop((0, 0, w, bottom)).save(path)
    return True


if __name__ == "__main__":
    sys.stdout.reconfigure(encoding="utf-8")
    n = 0
    for p in sorted(IMG.glob("*.png")):
        if clean(p):
            n += 1
    print(f"{n} ảnh đã cắt bớt phần trắng thừa")
