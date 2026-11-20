import sys
from PIL import Image, ImageDraw
if len(sys.argv) != 2:
    sys.exit("usage: python3 -I hero.py <dir with l1.png, rtop.png and rbot.png>")
S = sys.argv[1]
W, H, K = 1600, 800, 4
bg = (8, 9, 12)
blue = (72, 128, 215)
out = Image.new("RGB", (W, H), bg)
out.paste(Image.open(f"{S}/rtop.png").convert("RGB"), (1100, 0))
out.paste(Image.open(f"{S}/rbot.png").convert("RGB"), (1100, 400))
left = Image.open(f"{S}/l1.png").convert("RGB").crop((0, 0, 1100, 800))
x0, x1 = 1040, 1100
m = Image.new("L", (W * K, H * K), 0)
ImageDraw.Draw(m).polygon([(0, 0), (x0 * K, 0), (x1 * K, H * K), (0, H * K)], fill=255)
m = m.resize((W, H), Image.LANCZOS)
full = Image.new("RGB", (W, H), bg)
full.paste(left, (0, 0))
out = Image.composite(full, out, m)
lines = Image.new("L", (W * K, H * K), 0)
d = ImageDraw.Draw(lines)
d.line([(x0 * K, -K), (x1 * K, H * K + K)], fill=255, width=3 * K)
xm = (x0 + (x1 - x0) * 0.5) * K
d.line([(xm, 400 * K), (W * K, 400 * K)], fill=255, width=3 * K)
lines = lines.resize((W, H), Image.LANCZOS)
out = Image.composite(Image.new("RGB", (W, H), blue), out, lines)
out.save(f"{S}/hero.png")
