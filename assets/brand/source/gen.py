"""Generate the SuiteWard brand SVGs into ../svg, outlining the wordmark from ../fonts."""
from pathlib import Path

from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

BRAND = Path(__file__).resolve().parent.parent
FONTS = BRAND / "fonts"
OUT = BRAND / "svg"
OUT.mkdir(parents=True, exist_ok=True)

INK, GREEN, PAPER = "#12202F", "#3DD6A0", "#F4F1EA"
SHIELD = "M50 5 L88 18 V47 C88 70 72 87 50 95 C28 87 12 70 12 47 V18 Z"


def mark(stroke, bar, check, ox=0, oy=0):
    """Shield group in a 100x100 box; bar is the bar height, check the check width."""
    r = bar / 2
    y1, y2, y3 = 31, 45, 59
    return (
        f'<g transform="translate({ox} {oy})">'
        f'<path d="{SHIELD}" fill="{INK}" stroke="{GREEN}" stroke-width="{stroke}" stroke-linejoin="round"/>'
        f'<rect x="29" y="{y1}" width="42" height="{bar}" rx="{r}" fill="{PAPER}"/>'
        f'<rect x="29" y="{y2}" width="42" height="{bar}" rx="{r}" fill="{PAPER}"/>'
        f'<rect x="29" y="{y3}" width="20" height="{bar}" rx="{r}" fill="{PAPER}"/>'
        f'<path d="M55 {y3 + 4} L61 {y3 + 10} L72 {y3 - 2}" fill="none" stroke="{GREEN}" '
        f'stroke-width="{check}" stroke-linecap="round" stroke-linejoin="round"/></g>'
    )


def svg(w, h, body, title):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w}" height="{h}" '
        f'role="img" aria-label="{title}"><title>{title}</title>{body}</svg>\n'
    )


def pair_kern(font, left, right):
    """Horizontal kerning of a glyph pair from GPOS PairPos lookups, in font units."""
    gpos = font["GPOS"].table
    for lookup in gpos.LookupList.Lookup:
        for sub in lookup.SubTable:
            if lookup.LookupType == 9:
                sub = sub.ExtSubTable
            if getattr(sub, "LookupType", 2) != 2 or left not in sub.Coverage.glyphs:
                continue
            if sub.Format == 1:
                i = sub.Coverage.glyphs.index(left)
                for rec in sub.PairSet[i].PairValueRecord:
                    if rec.SecondGlyph == right:
                        return getattr(rec.Value1, "XAdvance", 0) or 0
            else:
                c1 = sub.ClassDef1.classDefs.get(left, 0)
                c2 = sub.ClassDef2.classDefs.get(right, 0)
                v = sub.Class1Record[c1].Class2Record[c2].Value1
                kern = getattr(v, "XAdvance", 0) if v else 0
                if kern:
                    return kern
    return 0


def text_paths(runs, size, x0, baseline):
    """Outline runs of (text, weight); returns (svg path d strings, end x)."""
    x, prev = x0, None
    out = []
    for text, weight in runs:
        font = TTFont(FONTS / {400: "Inter-Regular.woff", 600: "Inter-SemiBold.woff"}[weight])
        cmap, glyphs, upm = font.getBestCmap(), font.getGlyphSet(), font["head"].unitsPerEm
        s = size / upm
        for ch in text:
            g = cmap[ord(ch)]
            if prev:
                x += pair_kern(font, prev, g) * s
            pen = SVGPathPen(glyphs)
            glyphs[g].draw(TransformPen(pen, (s, 0, 0, -s, x, baseline)))
            out.append(pen.getCommands())
            x += glyphs[g].width * s
            prev = g
    return out, x


RUNS = [("Suite", 400), ("Ward", 600)]

# Mark: master and small-size variant (thicker outline and bars, for 16-48 px).
(OUT / "suiteward-mark.svg").write_text(svg(100, 100, mark(4, 7, 6), "SuiteWard"), newline=chr(10))
(OUT / "suiteward-mark-small.svg").write_text(svg(100, 100, mark(7, 9, 7.5), "SuiteWard"), newline=chr(10))

# Horizontal logo: mark at 100 high, text cap height ~46 aligned to the shield's optical centre.
size = 64
for name, color in (("suiteward-logo.svg", INK), ("suiteward-logo-dark.svg", PAPER)):
    paths, end = text_paths(RUNS, size, 124, 74)
    body = mark(4, 7, 6) + "".join(f'<path d="{d}" fill="{color}"/>' for d in paths)
    (OUT / name).write_text(svg(round(end + 4), 100, body, "SuiteWard"), newline=chr(10))

print("ok", sorted(p.name for p in OUT.glob("*.svg")))
