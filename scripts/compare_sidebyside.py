#!/usr/bin/env python3
"""Build an HTML side-by-side view of a run's translation for line-by-line review.

Two modes:

  2-column (EN | our FR), aligned per segment — the reliable view:
      python scripts/compare_sidebyside.py runs/<run-id>/segments.jsonl out.html

  3-column (EN | our FR | reference FR), aligned per paragraph — add a reference
  EPUB. NB: a reference of a different edition has its own segmentation, so the
  third column drifts down the page; read it by blocks, not row-by-row:
      python scripts/compare_sidebyside.py runs/<run-id>/segments.jsonl out.html \
          --ref books/lumen/ch1/fr-ch1.epub --ref-keep index_split_00 \
          --our runs/<run-id>/translated.epub --src books/lumen/ch1/en-ch1.epub \
          --keep chapter_1

The 2-column mode reads segments.jsonl directly (source_text/target_text). The
3-column mode reads <p> paragraphs from the three EPUBs in document order.
"""
import argparse
import html
import json
import re
import zipfile


def paras(epub: str, keep) -> list[str]:
    z = zipfile.ZipFile(epub)
    names = sorted(
        n
        for n in z.namelist()
        if n.lower().endswith((".xhtml", ".html", ".htm"))
        and (not keep or any(k in n for k in keep))
    )
    out = []
    for n in names:
        raw = z.read(n).decode("utf-8", "ignore")
        for m in re.findall(r"<p\b[^>]*>(.*?)</p>", raw, re.S):
            txt = html.unescape(re.sub(r"<[^>]+>", "", m)).strip()
            if txt:
                out.append(txt)
    return out


HEAD = (
    "<!doctype html><meta charset=utf-8><title>{title}</title>"
    "<style>body{{font:14px/1.5 system-ui;margin:1.5rem}}"
    "table{{border-collapse:collapse;width:100%}}"
    "td{{border:1px solid #ccc;padding:.5rem;vertical-align:top}}"
    "th{{background:#222;color:#fff;padding:.5rem;position:sticky;top:0}}"
    "tr:nth-child(even) td{{background:#f7f7f7}}.idx{{color:#999;font-size:11px}}</style>"
)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("segments")
    ap.add_argument("out")
    ap.add_argument("--ref", help="reference EPUB for a 3rd column")
    ap.add_argument("--ref-keep", nargs="*", default=[], help="href substrings to keep in --ref")
    ap.add_argument("--our", help="our translated EPUB (3-col mode, paragraph-aligned)")
    ap.add_argument("--src", help="source EPUB (3-col mode)")
    ap.add_argument("--keep", nargs="*", default=[], help="href substrings to keep in --our/--src")
    a = ap.parse_args()

    if not a.ref:
        rows = [json.loads(line) for line in open(a.segments) if line.strip()]
        h = [HEAD.format(title="EN | notre FR"), "<table><tr><th>EN (source)</th><th>Notre FR</th></tr>"]
        for r in rows:
            s = html.escape(r.get("source_text", ""))
            t = html.escape(r.get("target_text", ""))
            if not s.strip() and not t.strip():
                continue
            h.append(f"<tr><td>{s}</td><td>{t}</td></tr>")
        h.append("</table>")
        open(a.out, "w", encoding="utf-8").write("\n".join(h))
        print(f"wrote {a.out} | {len(rows)} segments (2-col)")
        return

    en = paras(a.src, a.keep) if a.src else []
    ourfr = paras(a.our, a.keep) if a.our else []
    ref = paras(a.ref, a.ref_keep)
    n = max(len(en), len(ourfr), len(ref))

    def g(lst, i):
        return html.escape(lst[i]) if i < len(lst) else ""

    h = [
        HEAD.format(title="EN | notre FR | réf"),
        f"<p>EN: {len(en)} §, notre FR: {len(ourfr)} §, réf: {len(ref)} § — "
        "alignement positionnel, la colonne réf peut dériver.</p>",
        "<table><tr><th>EN (source)</th><th>Notre FR</th><th>Réf FR</th></tr>",
    ]
    for i in range(n):
        h.append(
            f"<tr><td><span class=idx>{i}</span><br>{g(en, i)}</td>"
            f"<td>{g(ourfr, i)}</td><td>{g(ref, i)}</td></tr>"
        )
    h.append("</table>")
    open(a.out, "w", encoding="utf-8").write("\n".join(h))
    print(f"wrote {a.out} | EN/ourFR/ref = {len(en)}/{len(ourfr)}/{len(ref)} (3-col)")


if __name__ == "__main__":
    main()
