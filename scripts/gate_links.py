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
    gate_links.py [корень]
"""

import pathlib
import re
import sys
import unicodedata
from urllib.parse import urlsplit

LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
# Заголовок разбирается без регулярного выражения. Выражение вида
# «^(#{1,6})\s+(.*)$» выглядит безобидно, но и «\s+», и «.*» претендуют на
# одни и те же пробелы: разбор становится неоднозначным, и на строке из
# сотен решёток движок перебирает откаты вместо работы. Посимвольный разбор
# делает то же самое за один проход и читается не хуже.
FENCE = re.compile(r"^\s*(```|~~~)")
SKIP_DIRS = {".git", "node_modules", "corpus", "dist", "bin", ".terraform"}

# Схемы, на которых проверка останавливается, причина в описании модуля.
# Схема сравнивается разобранной, а не началом строки: запись вида «HTTPS://»
# тоже внешняя, а литерал с «://» в коде анализатор справедливо принимает за
# обращение по незащищённому протоколу, хотя мы сюда как раз не ходим.
EXTERNAL_SCHEMES = frozenset({"http", "https", "mailto", "tel"})


def heading_parts(line):
    """Разбирает строку заголовка: сколько решёток и что после них.

    Возвращает пару «уровень, текст». Уровень ноль означает, что строка
    заголовком не является: решёток нет, их больше шести или за ними не
    стоит пробел.
    """
    level = 0
    while level < len(line) and line[level] == "#":
        level += 1
    if level == 0 or level > 6:
        return 0, ""
    rest = line[level:]
    if not rest[:1].isspace():
        return 0, ""
    return level, rest.lstrip()


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


def heading_text(raw):
    """Срезает хвост закрытого заголовка: решётки в конце и пробелы перед ними.

    Порядок важен: решётки считаются хвостом, только когда стоят в самом
    конце, поэтому сначала снимаются они, а уже потом отступ перед ними.
    """
    return raw.rstrip("#").rstrip()


def anchors_of(path):
    """Собирает якоря документа — все заголовки вне блоков кода.

    Решётка внутри ограждения это строка примера, а не заголовок, и якоря
    она не даёт: иначе ссылка на выдуманный раздел прошла бы проверку.
    Повторы получают числовой хвост, потому что так их различает GitHub.
    """
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
        level, text = heading_parts(line)
        if not level:
            continue
        base = anchor(heading_text(text))
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
    """Собирает ссылки документа вместе с номерами строк.

    Блоки кода пропускаются по той же причине, что и в разборе заголовков:
    ссылка в примере никуда не ведёт по замыслу. Внешние схемы отсеиваются
    выше, в check_doc, — здесь важен один проход по файлу.
    """
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


def has_anchor(dest, frag, cache):
    """Ищет якорь и в нормализованном виде, и как написано в ссылке.

    Разбор заголовков дорогой, а на один документ ссылаются десятки раз,
    поэтому результат держится в общем на прогон кеше.
    """
    if dest not in cache:
        cache[dest] = anchors_of(dest)
    known = cache[dest]
    return anchor(frag) in known or frag in known


def link_problem(doc, rel, line_no, target, cache):
    """Проверяет одну ссылку и возвращает описание поломки либо None."""
    path_part, _, frag = target.partition("#")
    dest = (doc.parent / path_part).resolve() if path_part else doc

    if not dest.exists():
        return f"{rel}:{line_no}: нет файла {target}"

    # Якорь спрашиваем только у документов: у картинок и исходников его нет,
    # и требовать там заголовок значит выдумывать поломку.
    if frag and dest.suffix == ".md" and not has_anchor(dest, frag, cache):
        return f"{rel}:{line_no}: нет якоря #{frag} в {path_part or rel.name}"

    return None


def check_doc(doc, rel, cache):
    """Проверяет ссылки одного документа: сколько проверено и что поломано."""
    checked = 0
    broken = []
    for line_no, target in links_of(doc):
        if urlsplit(target).scheme.lower() in EXTERNAL_SCHEMES:
            continue
        checked += 1
        problem = link_problem(doc, rel, line_no, target, cache)
        if problem:
            broken.append(problem)
    return checked, broken


def main():
    """Обходит документы от корня и считает битые ссылки.

    Отчёт собирается целиком, а не обрывается на первой поломке: починить
    десять ссылок за один заход дешевле, чем десять раз ждать ворота.
    """
    root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
    docs = sorted(
        p for p in root.rglob("*.md")
        if not any(part in SKIP_DIRS for part in p.parts)
    )

    anchor_cache = {}
    broken = []
    checked = 0

    for doc in docs:
        found, problems = check_doc(doc, doc.relative_to(root), anchor_cache)
        checked += found
        broken.extend(problems)

    if broken:
        print(f"Битых ссылок {len(broken)} из {checked} проверенных:")
        for line in broken:
            print(f"  {line}")
        return 1

    print(f"Ссылок проверено {checked} в {len(docs)} документах, битых нет.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
