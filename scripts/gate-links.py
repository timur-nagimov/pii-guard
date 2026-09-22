#!/usr/bin/env python3
"""Проверка относительных ссылок во всех документах проекта.

Битая ссылка в документации стоит дёшево, пока её читает автор, и дорого,
когда её открывает жюри. Проверка занимает доли секунды, поэтому её место
в воротах, а не в списке добрых намерений.

Проверяется существование файла и существование якоря. Якоря считаются по
правилам GitHub: нижний регистр, пробелы в дефисы, знаки препинания прочь,
повторы с числовым хвостом. Кириллица сохраняется, GitHub её не выбрасывает.

Ссылки на внешние ресурсы не проверяются: сеть в закрытом контуре может
быть недоступна, и ворота стали бы зависеть от неё.

Использование:
    gate-links.py [корень]
"""

import pathlib
import re
import sys
import unicodedata

LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*$")
FENCE = re.compile(r"^\s*(```|~~~)")
SKIP_DIRS = {".git", "node_modules", "corpus", "dist", "bin", ".terraform"}


def anchor(text):
    """Превращает заголовок в якорь по правилам GitHub."""
    text = re.sub(r"`([^`]*)`", r"\1", text)
    text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)
    text = re.sub(r"[*_~]", "", text)
    out = []
    for ch in text.strip().lower():
        if ch.isalnum() or ch in "-_":
            out.append(ch)
        elif ch.isspace():
            out.append("-")
        elif unicodedata.category(ch).startswith("M"):
            out.append(ch)
    return "".join(out)


def anchors_of(path):
    found = {}
    inside = False
    try:
        lines = path.read_text(encoding="utf-8").split("\n")
    except OSError:
        return found
    for line in lines:
        if FENCE.match(line):
            inside = not inside
            continue
        if inside:
            continue
        m = HEADING.match(line)
        if not m:
            continue
        base = anchor(m.group(2))
        if not base:
            continue
        n = found.get(base, 0)
        found[base] = n + 1
    result = set()
    for base, count in found.items():
        result.add(base)
        for i in range(1, count):
            result.add(f"{base}-{i}")
    return result


def links_of(path):
    out = []
    inside = False
    try:
        lines = path.read_text(encoding="utf-8").split("\n")
    except OSError:
        return out
    for n, line in enumerate(lines, 1):
        if FENCE.match(line):
            inside = not inside
            continue
        if inside:
            continue
        for m in LINK.finditer(line):
            out.append((n, m.group(1)))
    return out


def main():
    root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
    docs = [
        p for p in root.rglob("*.md")
        if not any(part in SKIP_DIRS for part in p.parts)
    ]

    anchor_cache = {}
    broken = []
    checked = 0

    for doc in sorted(docs):
        for line_no, target in links_of(doc):
            if target.startswith(("http://", "https://", "mailto:", "tel:")):
                continue
            checked += 1

            path_part, _, frag = target.partition("#")
            if path_part:
                dest = (doc.parent / path_part).resolve()
            else:
                dest = doc

            rel = doc.relative_to(root)
            if not dest.exists():
                broken.append(f"{rel}:{line_no}: нет файла {target}")
                continue

            if frag and dest.suffix == ".md":
                if dest not in anchor_cache:
                    anchor_cache[dest] = anchors_of(dest)
                if anchor(frag) not in anchor_cache[dest] and frag not in anchor_cache[dest]:
                    broken.append(f"{rel}:{line_no}: нет якоря #{frag} в {path_part or rel.name}")

    if broken:
        print(f"Битых ссылок {len(broken)} из {checked} проверенных:")
        for line in broken:
            print(f"  {line}")
        return 1

    print(f"Ссылок проверено {checked} в {len(docs)} документах, битых нет.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
