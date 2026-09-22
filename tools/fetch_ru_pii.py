#!/usr/bin/env python3
"""Сборщик независимого русскоязычного набора wolframko/russian-pii-66k.

Зачем нужен: собственный генератор проверяет сервис на данных, которые мы сами
и придумали. Здесь разметка чужая, её делали без оглядки на наши детекторы,
поэтому качество на ней честнее.

Что делает скрипт:
  1. Забирает строки набора с huggingface.co. Основной путь, это файл parquet
     (одна загрузка на весь набор). Запасной путь, это постраничная выдача
     datasets-server по сто строк за вызов.
  2. Переводит метки источника в наши типы, склеивает соседние компоненты
     имени и адреса.
  3. Переводит смещения из символов в байты и проверяет каждое смещение.
  4. Пишет результат в формате нашего набора (строка JSON на элемент).

Запуск:
    python3 tools/fetch_ru_pii.py --out corpus/ru_pii_66k.jsonl \
        --sample testdata/sample_ru_pii.jsonl
"""

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter

DATASET = "wolframko/russian-pii-66k"
PARQUET_INDEX = f"https://huggingface.co/api/datasets/{urllib.parse.quote(DATASET)}/parquet"
ROWS_API = "https://datasets-server.huggingface.co/rows"
PAGE = 100  # жёсткое ограничение datasets-server на число строк за вызов

# Соответствие меток источника нашим типам.
#
# Часть соответствий уточнена по фактическим значениям, а не по названию метки:
#   IDCARDNUM хранит российский внутренний паспорт вида "29 37 900581",
#   SOCIALNUM хранит СНИЛС вида "511-615-594 07",
#   TAXNUM хранит ИНН физлица из двенадцати цифр.
LABEL_MAP = {
    "GIVENNAME": "FIO",
    "SURNAME": "FIO",
    "MIDDLENAME": "FIO",
    "STREET": "ADDRESS",
    "BUILDINGNUM": "ADDRESS",
    "CITY": "ADDRESS",
    "SECONDARYADDRESS": "ADDRESS",
    "ZIPCODE": "POSTCODE",
    "EMAIL": "EMAIL",
    "TELEPHONENUM": "PHONE",
    "DATEOFBIRTH": "DOB",
    "CREDITCARDNUMBER": "CARD",
    "CREDITCARDCVV": "CVV",
    "PASSPORTNUM": "PASSPORT",
    "IDCARDNUM": "PASSPORT",
    "SOCIALNUM": "SNILS",
    "DRIVERLICENSENUM": "DRIVER_LICENSE",
    "TAXNUM": "INN",
}

# Метки, которых нет в нашем перечне типов. В разметку они не попадают, но
# из-за них элемент помечается partial_labels, иначе замаскированный сервисом
# номер счёта или пароль засчитается как ложное срабатывание.
UNSUPPORTED = {"USERNAME", "ACCOUNTNUM", "PASSWORD", "URL", "IP", "IBAN",
               "ACCOUNTNUMBER", "BITCOINADDRESS", "AGE", "GENDER"}

# Метки, соседние фрагменты которых склеиваются в один фрагмент нашего набора.
NAME_LABELS = {"GIVENNAME", "SURNAME", "MIDDLENAME"}
ADDRESS_LABELS = {"STREET", "BUILDINGNUM", "CITY", "SECONDARYADDRESS"}

# Разделитель, через который склеивание допустимо. Это пробелы, запятые и
# служебные сокращения адреса. Через прозаические вставки вида " по адресу "
# или " в городе " склеивать нельзя: они не персональные данные.
ADDRESS_GLUE = re.compile(
    r"^[\s,;:]*(?:(?:ул|улица|д|дом|влд|г|город|гор|к|корп|корпус|кв|квартира"
    r"|оф|офис|стр|строение|пом|литер|мкр|пос|пгт|снт)\.?[\s,;:]*)*$",
    re.IGNORECASE,
)
NAME_GLUE = re.compile(r"^ ?$")

TIMEOUT = 120


def log(msg):
    print(msg, file=sys.stderr, flush=True)


def http_json(url, retries=6):
    """Забирает JSON, переживая временные отказы сети и троттлинг источника."""
    last = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "pii-guard-corpus/1.0"})
            with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, OSError) as err:
            last = err
            time.sleep(2 * (attempt + 1))
    raise RuntimeError(f"не удалось получить {url}: {last}")


def fetch_parquet(limit, cache):
    """Основной путь: один файл parquet на весь набор.

    Требует pyarrow. Если его нет, вызывающий код переходит на постраничную
    выдачу.
    """
    import pyarrow.parquet as pq  # импорт внутри, чтобы отсутствие не ломало запасной путь

    if not os.path.exists(cache):
        index = http_json(PARQUET_INDEX)
        url = index["default"]["train"][0]
        log(f"качаю parquet: {url}")
        req = urllib.request.Request(url, headers={"User-Agent": "pii-guard-corpus/1.0"})
        with urllib.request.urlopen(req, timeout=TIMEOUT) as resp, open(cache, "wb") as out:
            while True:
                chunk = resp.read(1 << 20)
                if not chunk:
                    break
                out.write(chunk)
    else:
        log(f"беру parquet из кэша: {cache}")

    table = pq.read_table(cache, columns=["source_text", "privacy_mask", "language"])
    rows = table.to_pylist()
    if limit:
        rows = rows[:limit]
    return rows


def fetch_rows_api(limit):
    """Запасной путь: постраничная выдача по сто строк за вызов."""
    rows = []
    offset = 0
    total = limit or 10_000
    while offset < total:
        length = min(PAGE, total - offset)
        url = (f"{ROWS_API}?dataset={urllib.parse.quote(DATASET, safe='')}"
               f"&config=default&split=train&offset={offset}&length={length}")
        data = http_json(url)
        if not limit:
            total = data.get("num_rows_total", total)
        batch = data.get("rows", [])
        if not batch:
            break
        rows.extend(item["row"] for item in batch)
        offset += len(batch)
        if offset % 2000 == 0:
            log(f"получено строк: {offset} из {total}")
        time.sleep(0.2)  # пауза, чтобы не ловить троттлинг
    return rows[:limit] if limit else rows


def byte_prefix(text):
    """Строит перевод номера символа в байтовое смещение.

    В Python срезы строк идут по символам, в Go по байтам, а кириллическая
    буква занимает два байта, поэтому смещения источника нельзя переносить
    в набор как есть.
    """
    prefix = [0] * (len(text) + 1)
    total = 0
    for i, ch in enumerate(text):
        total += len(ch.encode("utf-8"))
        prefix[i + 1] = total
    return prefix


def merge_runs(parts, text, labels, glue):
    """Склеивает соседние фрагменты одного смысла в один фрагмент.

    Склеивание идёт только через допустимый разделитель: имя и фамилия через
    пробел, компоненты адреса через запятую и сокращение вида "д." или "ул.".
    """
    out = []
    for part in parts:
        prev = out[-1] if out else None
        joinable = (
            prev is not None
            and prev["label"] in labels
            and part["label"] in labels
            and prev["type"] == part["type"]
            and prev["end"] <= part["start"]
            and glue.match(text[prev["end"]:part["start"]])
        )
        if joinable:
            prev["end"] = part["end"]
            prev["label"] = part["label"]
        else:
            out.append(dict(part))
    return out


def convert(row, stats):
    """Превращает строку источника в элемент нашего набора.

    Возвращает (spans_в_байтах, partial_labels) либо None, если строку надо
    выбросить.
    """
    text = row.get("source_text") or ""
    if not text.strip():
        stats["пустой текст"] += 1
        return None

    mask = row.get("privacy_mask") or []
    prefix = byte_prefix(text)
    parts = []
    partial = False

    for item in sorted(mask, key=lambda m: (m.get("start", 0), m.get("end", 0))):
        label = item.get("label") or ""
        start, end, value = item.get("start"), item.get("end"), item.get("value") or ""
        our = LABEL_MAP.get(label)
        if our is None:
            # Метки вне нашего перечня в разметку не идут, но делают её неполной.
            partial = True
            if label not in UNSUPPORTED:
                stats[f"неизвестная метка {label}"] += 1
            continue
        if start is None or end is None or not (0 <= start < end <= len(text)):
            stats["границы вне текста"] += 1
            partial = True
            continue
        # Проверка разметки источника в символах, до перевода в байты.
        if text[start:end] != value:
            stats["значение не совпало (символы)"] += 1
            partial = True
            continue
        parts.append({"start": start, "end": end, "type": our, "label": label, "value": value})

    parts = merge_runs(parts, text, NAME_LABELS, NAME_GLUE)
    parts = merge_runs(parts, text, ADDRESS_LABELS, ADDRESS_GLUE)

    raw = text.encode("utf-8")
    spans = []
    for part in parts:
        bs, be = prefix[part["start"]], prefix[part["end"]]
        # Обязательная самопроверка: срез байтов обязан декодироваться ровно
        # в тот фрагмент текста, который размечен в символах.
        try:
            decoded = raw[bs:be].decode("utf-8")
        except UnicodeDecodeError:
            stats["срез байтов не декодируется"] += 1
            return None
        if decoded != text[part["start"]:part["end"]]:
            stats["срез байтов не совпал"] += 1
            return None
        spans.append({"start": bs, "end": be, "type": part["type"]})

    if not spans:
        stats["нет ни одного фрагмента"] += 1
        return None
    return spans, partial


def main():
    ap = argparse.ArgumentParser(description="сборка набора russian-pii-66k в наш формат")
    ap.add_argument("--out", default="corpus/ru_pii_66k.jsonl", help="куда писать набор")
    ap.add_argument("--sample", default="testdata/sample_ru_pii.jsonl",
                    help="куда писать пробу для тестов без сети")
    ap.add_argument("--sample-size", type=int, default=200, help="размер пробы")
    ap.add_argument("--limit", type=int, default=0, help="взять не больше стольких строк источника")
    ap.add_argument("--cache", default=os.path.join(os.environ.get("TMPDIR", "/tmp"), "ru_pii_66k.parquet"),
                    help="файл кэша parquet")
    ap.add_argument("--rows-api", action="store_true", help="принудительно идти постраничной выдачей")
    args = ap.parse_args()

    rows = None
    if not args.rows_api:
        try:
            rows = fetch_parquet(args.limit, args.cache)
            log(f"parquet прочитан, строк: {len(rows)}")
        except ImportError:
            log("pyarrow не найден, перехожу на постраничную выдачу")
        except Exception as err:  # noqa: BLE001 - любой сбой основного пути ведёт на запасной
            log(f"parquet не получился ({err}), перехожу на постраничную выдачу")
    if rows is None:
        rows = fetch_rows_api(args.limit)
        log(f"постраничная выдача завершена, строк: {len(rows)}")

    stats = Counter()
    by_type = Counter()
    written = 0
    partial_count = 0
    dropped = 0

    for path in (args.out, args.sample):
        os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)

    with open(args.out, "w", encoding="utf-8") as out:
        for i, row in enumerate(rows):
            got = convert(row, stats)
            if got is None:
                dropped += 1
                continue
            spans, partial = got
            item = {
                "id": f"hf-ru-pii-{i:06d}",
                "category": "hf_ru_pii",
                "source": "hf_ru_pii",
                "text": row["source_text"],
                "spans": spans,
                "partial_labels": partial,
            }
            out.write(json.dumps(item, ensure_ascii=False) + "\n")
            written += 1
            partial_count += int(partial)
            for span in spans:
                by_type[span["type"]] += 1

    # Проба кладётся в репозиторий, чтобы тесты работали без сети.
    with open(args.out, encoding="utf-8") as src, open(args.sample, "w", encoding="utf-8") as dst:
        for n, line in enumerate(src):
            if n >= args.sample_size:
                break
            dst.write(line)

    total = len(rows)
    log("")
    log(f"прочитано строк источника: {total}")
    log(f"записано элементов:        {written}")
    log(f"выброшено элементов:       {dropped} ({100 * dropped / max(total, 1):.2f}%)")
    log(f"с partial_labels:          {partial_count} ({100 * partial_count / max(written, 1):.1f}%)")
    log("")
    log("фрагментов по типам:")
    for name, count in by_type.most_common():
        log(f"  {name:16s} {count}")
    if stats:
        log("")
        log("причины отбраковки фрагментов и строк:")
        for name, count in stats.most_common():
            log(f"  {name:32s} {count}")


if __name__ == "__main__":
    main()
