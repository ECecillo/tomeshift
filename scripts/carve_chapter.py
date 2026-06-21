#!/usr/bin/env python3
"""Carve a single chapter (or any subset of spine documents) out of an EPUB.

Produces a minimal valid EPUB containing only the chosen text documents, so a
full-book LLM run can be reduced to one cheap chapter for review/evaluation.
Navigation (nav/ncx) and all CSS/images are kept; other text documents are
dropped from both the manifest and the spine.

Usage:
    python scripts/carve_chapter.py SRC.epub OUT.epub HREF_SUBSTR [HREF_SUBSTR ...]

HREF_SUBSTR are substrings matched against manifest hrefs of text documents to
KEEP. Inspect the spine first to find them, e.g.:

    # English WN volume: one file per chapter
    python scripts/carve_chapter.py "vol1.epub" en-ch1.epub chapter_1.xhtml

    # An auto-split EPUB where chapter 1 spans several files
    python scripts/carve_chapter.py "vf.epub" fr-ch1.epub \
        index_split_004 index_split_005 index_split_006 \
        index_split_007 index_split_008 index_split_009
"""
import os
import re
import shutil
import sys
import zipfile


def is_text(href: str) -> bool:
    return href.lower().endswith((".xhtml", ".html", ".htm"))


def main() -> None:
    if len(sys.argv) < 4:
        sys.exit(__doc__)
    src, out, keep_subs = sys.argv[1], sys.argv[2], sys.argv[3:]

    work = out + ".work"
    if os.path.exists(work):
        shutil.rmtree(work)
    os.makedirs(work)
    with zipfile.ZipFile(src) as z:
        z.extractall(work)

    opf = next(
        os.path.join(r, f)
        for r, _, fs in os.walk(work)
        for f in fs
        if f.endswith(".opf")
    )
    opfdir = os.path.dirname(opf)
    t = open(opf, encoding="utf-8").read()

    items = []  # (id, href)
    for m in re.finditer(r"<item\b[^>]*?/?>", t):
        tag = m.group(0)
        i = re.search(r'id="([^"]+)"', tag)
        h = re.search(r'href="([^"]+)"', tag)
        if i and h:
            items.append((i.group(1), h.group(1)))

    def keep_href(href: str) -> bool:
        if not is_text(href):
            return True  # keep css/img/ncx/nav
        low = href.lower()
        if "nav" in low or "toc" in low:
            return True
        return any(s in href for s in keep_subs)

    kept_ids = {i for (i, h) in items if keep_href(h)}
    drop_files = [h for (i, h) in items if is_text(h) and i not in kept_ids]

    def spine_repl(mobj):
        body = mobj.group(2)
        refs = re.findall(r"<itemref\b[^>]*?/?>", body)
        kept = [
            r
            for r in refs
            if (m := re.search(r'idref="([^"]+)"', r)) and m.group(1) in kept_ids
        ]
        return "<spine" + mobj.group("attrs") + ">" + "".join(kept) + "</spine>"

    t = re.sub(r"<spine(?P<attrs>[^>]*)>(.*?)</spine>", spine_repl, t, flags=re.S)

    def manifest_repl(mobj):
        body = mobj.group(2)
        tags = re.findall(r"<item\b[^>]*?/?>", body)
        kept = [
            tg
            for tg in tags
            if (m := re.search(r'id="([^"]+)"', tg)) and m.group(1) in kept_ids
        ]
        return mobj.group(1) + "".join(kept) + "</manifest>"

    t = re.sub(r"(<manifest[^>]*>)(.*?)</manifest>", manifest_repl, t, flags=re.S)
    open(opf, "w", encoding="utf-8").write(t)

    for h in drop_files:
        p = os.path.join(opfdir, h)
        if os.path.exists(p):
            os.remove(p)

    if os.path.exists(out):
        os.remove(out)
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        mt = os.path.join(work, "mimetype")
        if os.path.exists(mt):
            z.write(mt, "mimetype", compress_type=zipfile.ZIP_STORED)
        for r, _, fs in os.walk(work):
            for f in fs:
                full = os.path.join(r, f)
                rel = os.path.relpath(full, work)
                if rel == "mimetype":
                    continue
                z.write(full, rel)
    shutil.rmtree(work)
    kept_text = [h for (i, h) in items if is_text(h) and i in kept_ids]
    print(f"wrote {out} | kept text docs: {kept_text}")


if __name__ == "__main__":
    main()
