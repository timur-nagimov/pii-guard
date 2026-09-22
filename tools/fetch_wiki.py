#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Сбор настоящего русского текста из Википедии для проверки качества маскирования.

Зачем: синтетический набор проверяет нас на данных, которые мы сами и придумали.
Энциклопедическая проза даёт то, чего генератор дать не может: живой порядок слов,
названия организаций, географию, числа и даты, которые персональными данными не
являются. Это честная проверка на ложные срабатывания.

Скрипт собирает два вида элементов:

1. wiki_negative  - предложения, в которых персональных данных заведомо нет.
   spans пустой, partial_labels false. Любое изменение такого текста маскировщиком
   является ложным срабатыванием.
2. wiki_carrier   - осмысленные предложения длиной от 60 до 400 символов, которые
   служат реалистичным фоном для вставки синтетических значений.
   spans пустой, partial_labels true, потому что неразмеченные имена в них есть.

Сеть используется вежливо: свой User-Agent и пауза между вызовами.
Сырые статьи складываются в кэш, поэтому пересборку набора можно делать без сети.

Запуск:
    python3 tools/fetch_wiki.py                       полный цикл, сеть плюс сборка
    python3 tools/fetch_wiki.py --build-only          пересборка из кэша, без сети
    python3 tools/fetch_wiki.py --batches 200 --full-articles 600
"""

import argparse
import json
import os
import random
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = "https://ru.wikipedia.org/w/api.php"
# Заголовок уходит в сеть и обязан быть в latin-1, поэтому описание тут по-английски:
# правила доступа Википедии требуют осмысленного User-Agent с назначением и контактом.
UA = (
    "pii-guard-eval-dataset/1.0 "
    "(open Russian text collection for PII masking quality evaluation; "
    "contact: tna63925@gmail.com)"
)

CORPUS_DIR = "corpus"
TESTDATA_DIR = "testdata"
CACHE_PATH = os.path.join(CORPUS_DIR, "wiki_raw.jsonl")
NEGATIVE_PATH = os.path.join(CORPUS_DIR, "wiki_negative.jsonl")
CARRIER_PATH = os.path.join(CORPUS_DIR, "wiki_carrier.jsonl")

# ----------------------------------------------------------------------------
# Сеть
# ----------------------------------------------------------------------------


class Fetcher:
    """Обёртка над интерфейсом Википедии с паузой между вызовами."""

    def __init__(self, pause=0.6, retries=4):
        self.pause = pause
        self.retries = retries
        self.last_call = 0.0
        self.calls = 0

    def get(self, params):
        params = dict(params)
        params.setdefault("format", "json")
        params.setdefault("formatversion", "2")
        url = API + "?" + urllib.parse.urlencode(params)
        delay = 1.0
        for attempt in range(self.retries + 1):
            waited = time.time() - self.last_call
            if waited < self.pause:
                time.sleep(self.pause - waited)
            self.last_call = time.time()
            self.calls += 1
            try:
                req = urllib.request.Request(url, headers={"User-Agent": UA})
                with urllib.request.urlopen(req, timeout=45) as resp:
                    return json.loads(resp.read().decode("utf-8"))
            except (urllib.error.URLError, OSError, ValueError) as err:
                if attempt == self.retries:
                    print("  запрос не удался окончательно: %s" % err, file=sys.stderr)
                    return None
                time.sleep(delay)
                delay = min(delay * 2, 15.0)
        return None


def random_intros(fetcher, limit=20):
    """Двадцать случайных статей во вводной части. Один вызов - двадцать текстов."""
    data = fetcher.get(
        {
            "action": "query",
            "generator": "random",
            "grnnamespace": 0,
            "grnlimit": limit,
            "prop": "extracts|categories",
            "explaintext": 1,
            "exintro": 1,
            "exlimit": limit,
            "cllimit": 50,
        }
    )
    return _pages(data)


def random_pageids(fetcher, limit=10):
    """Случайные статьи целиком. Вводная часть первой приходит бесплатно."""
    data = fetcher.get(
        {
            "action": "query",
            "generator": "random",
            "grnnamespace": 0,
            "grnlimit": limit,
            "prop": "extracts|categories",
            "explaintext": 1,
            "cllimit": 50,
        }
    )
    return _pages(data)


def full_article(fetcher, pageid):
    """Полный текст одной статьи. Тело статьи даёт прозу, а не только первую фразу."""
    data = fetcher.get(
        {
            "action": "query",
            "pageids": pageid,
            "prop": "extracts|categories",
            "explaintext": 1,
            "cllimit": 50,
        }
    )
    pages = _pages(data)
    return pages[0] if pages else None


def _pages(data):
    if not data:
        return []
    out = []
    for page in data.get("query", {}).get("pages", []) or []:
        if page.get("missing") or not page.get("extract"):
            continue
        out.append(
            {
                "pageid": page.get("pageid"),
                "title": page.get("title", ""),
                "extract": page.get("extract", ""),
                "categories": [c.get("title", "") for c in page.get("categories", []) or []],
            }
        )
    return out


# ----------------------------------------------------------------------------
# Разбор текста
# ----------------------------------------------------------------------------

# Сокращения, после которых точка не заканчивает предложение.
ABBREV = set(
    """г гг в вв н э т д п пп см ср им св стр с рис табл обл р рр оз ул д кв корп
    млн млрд тыс трлн руб проф акад др пр сокр букв лат греч англ нем фр итал исп
    ок прим напр соотв кн ч гл м км мм кг мг шт экз яз ст тт мн ед род дат вин
    предл твор мл ст-л ж жен муж возв наст прош буд сущ прил числ мест нареч""".split()
)

HEADING_RE = re.compile(r"^\s*=+.*=+\s*$")
SENT_END_RE = re.compile(r"[.!?…]+")
WORD_RE = re.compile(r"[А-Яа-яЁёA-Za-z][А-Яа-яЁёA-Za-z\-]*")
CAP_WORD_RE = re.compile(r"\b[А-ЯЁA-Z][а-яёa-z\-]{1,}")
DIGIT_RUN_RE = re.compile(r"\d{7,}")
INITIAL_RE = re.compile(r"\b[А-ЯЁA-Z]\.")
PATRONYMIC_RE = re.compile(
    r"\b[А-ЯЁ][а-яё]+(?:ович|евич|ьевич|овна|евна|ьевна|ична|инична|оглы|кызы)\b",
    re.IGNORECASE,
)
# Куски, которые верно опознаются как персональные данные и потому в отрицательных
# примерах недопустимы.
CONTACT_RE = re.compile(
    r"(@|https?://|www\.|\+7|8\s*\(\s*9|\bтел\.|\bтелефон\b|\be-?mail\b|\bпочт[аеуой]\b"
    r"|\bпаспорт|\bснилс\b|\bинн\b|\bкпп\b|\bогрн\b|\bкарт[аыеу]\s*\d|\bcvv\b|\bпин-?код"
    r"|\bиндекс\s*\d|\bкв\.\s*\d|\bд\.\s*\d|\bдом\s*\d|\bул\.|\bулиц|\bпроспект|\bпереулок"
    r"|\bшоссе\b|\bнабережн|\bмикрорайон\b|\bводительск|\bвоенный билет\b|\bвид на жительство\b)",
    re.IGNORECASE,
)
# Полная дата вида 12.05.1980 или 12/05/1980 - типичная дата рождения.
FULL_DATE_RE = re.compile(r"\b\d{1,2}[./]\d{1,2}[./]\d{2,4}\b")
# Город с приставкой: г. Москва, с. Ивановка. Опознаётся как место рождения.
SETTLEMENT_RE = re.compile(r"\b(?:г|гор|с|д|пос|пгт|ст|х|аул|рп)\.\s*[А-ЯЁ]")
LATIN_RE = re.compile(r"[A-Za-z]")
CYRILLIC_RE = re.compile(r"[А-Яа-яЁё]")

PERSON_CAT_RE = re.compile(r"(Персоналии|Родившиеся|Умершие|тип:\s*человек|Похороненные)")
SERVICE_LINE_RE = re.compile(
    r"^(Известные носители|См\. также|Примечания|Литература|Ссылки|Источники|Библиография)\b"
)


def clean_lines(extract):
    """Режет выгрузку статьи на абзацы, выбрасывая заголовки и служебные списки."""
    out = []
    for raw in extract.split("\n"):
        line = raw.strip()
        if not line:
            continue
        if HEADING_RE.match(line):
            continue
        if SERVICE_LINE_RE.match(line):
            continue
        # Строки списка вида "Фамилия, Имя (1907-1984) - футболист" не проза.
        if line.count(",") >= 1 and line.count(" ") < 4:
            continue
        out.append(line)
    return out


def split_sentences(paragraph):
    """Делит абзац на предложения с оглядкой на русские сокращения и инициалы."""
    sentences = []
    start = 0
    pos = 0
    length = len(paragraph)
    while pos < length:
        match = SENT_END_RE.search(paragraph, pos)
        if not match:
            break
        end = match.end()
        if end >= length:
            break
        tail = paragraph[end:]
        stripped = tail.lstrip()
        shift = len(tail) - len(stripped)
        if shift == 0:
            # Точка внутри числа или сокращения без пробела.
            pos = end
            continue
        if not stripped:
            break
        head = paragraph[start:match.start()]
        last_word = WORD_RE.findall(head)
        last_word = last_word[-1] if last_word else ""
        if match.group(0) == "." and last_word.lower() in ABBREV:
            pos = end
            continue
        if match.group(0) == "." and len(last_word) == 1:
            # Инициал: "А. С. Пушкин".
            pos = end
            continue
        if not (stripped[0].isupper() or stripped[0].isdigit() or stripped[0] in "«\"("):
            pos = end
            continue
        sentences.append(paragraph[start:end].strip())
        start = end + shift
        pos = start
    tail = paragraph[start:].strip()
    if tail:
        sentences.append(tail)
    return sentences


def two_capitals_in_row(text):
    """Два подряд слова с заглавной буквы: признак полного имени или названия."""
    tokens = text.split()
    prev_cap = False
    for i, token in enumerate(tokens):
        word = token.strip("«»\"'()[],;:.!?—–-")
        if not word:
            prev_cap = False
            continue
        cap = word[0].isupper() and len(word) > 1 and word[1:].islower()
        if cap and prev_cap and i > 1:
            return True
        prev_cap = cap
    return False


def looks_russian(text):
    """Отсекает выгрузки на других языках: набор проверяет русскую речь."""
    cyr = len(CYRILLIC_RE.findall(text))
    lat = len(LATIN_RE.findall(text))
    return cyr >= 12 and cyr > lat * 2


def is_carrier(sentence):
    """Носитель: осмысленное предложение подходящей длины."""
    if not (60 <= len(sentence) <= 400):
        return False
    if not looks_russian(sentence):
        return False
    if len(WORD_RE.findall(sentence)) < 8:
        return False
    if sentence.count("(") != sentence.count(")"):
        return False
    if "@" in sentence or "http" in sentence:
        return False
    # Строка списка, а не фраза: много запятых и почти нет глагольной ткани.
    if sentence.count(",") > 8:
        return False
    return True


def negative_reason(sentence):
    """Возвращает причину отказа или None, если предложение годится в отрицательные.

    Проверки идут от простого к сложному, отбор нарочно строгий: цена ошибки здесь
    не пропущенное предложение, а испорченная мера ложных срабатываний.
    """
    if not (40 <= len(sentence) <= 300):
        return "длина"
    if not looks_russian(sentence):
        return "не русский"
    if len(WORD_RE.findall(sentence)) < 6:
        return "мало слов"
    if "@" in sentence:
        return "знак собаки"
    if DIGIT_RUN_RE.search(sentence):
        return "длинное число"
    if PATRONYMIC_RE.search(sentence):
        return "отчество"
    if INITIAL_RE.search(sentence):
        return "инициал"
    if CONTACT_RE.search(sentence):
        return "контакт или документ"
    if FULL_DATE_RE.search(sentence):
        return "полная дата"
    if SETTLEMENT_RE.search(sentence):
        return "населённый пункт с приставкой"
    if two_capitals_in_row(sentence):
        return "два заглавных подряд"
    if sentence.count(",") > 6:
        return "перечисление"
    if sentence.count("(") != sentence.count(")"):
        return "оборванная скобка"
    return None


def is_person_article(page):
    """Статья о человеке: из неё отрицательные примеры не берём совсем."""
    for cat in page.get("categories", []):
        if PERSON_CAT_RE.search(cat):
            return True
    title = page.get("title", "")
    # В русской Википедии биографии называются "Фамилия, Имя Отчество".
    if "," in title and not title.strip().endswith(")"):
        return True
    return False


def normalize_key(sentence):
    """Ключ для отсева повторов: регистр и пробелы не важны."""
    return re.sub(r"\s+", " ", sentence).strip().lower()


# ----------------------------------------------------------------------------
# Проверка смещений
# ----------------------------------------------------------------------------


def check_spans(text, spans):
    """Проверяет, что границы фрагментов заданы в байтах и режут текст ровно.

    В Python срезы строк идут по символам, в Go по байтам, кириллическая буква
    занимает два байта. Поэтому единственно верная проверка - декодировать срез
    байтового представления и сравнить его со значением фрагмента.
    """
    blob = text.encode("utf-8")
    for span in spans:
        start, end = span["start"], span["end"]
        if not (0 <= start < end <= len(blob)):
            return False
        try:
            piece = blob[start:end].decode("utf-8")
        except UnicodeDecodeError:
            return False
        if "value" in span and span["value"] != piece:
            return False
    return True


def make_item(item_id, category, text, spans, partial):
    if not check_spans(text, spans):
        raise ValueError("смещения фрагментов не совпадают с текстом: %s" % item_id)
    clean = [{"start": s["start"], "end": s["end"], "type": s["type"]} for s in spans]
    return {
        "id": item_id,
        "category": category,
        "source": "wikipedia",
        "text": text,
        "spans": clean,
        "partial_labels": partial,
    }


# ----------------------------------------------------------------------------
# Кэш сырых статей
# ----------------------------------------------------------------------------


def load_cache(path):
    pages = []
    seen = set()
    if not os.path.exists(path):
        return pages, seen
    with open(path, "r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                page = json.loads(line)
            except ValueError:
                continue
            pid = page.get("pageid")
            if pid in seen:
                continue
            seen.add(pid)
            pages.append(page)
    return pages, seen


def append_cache(handle, page):
    handle.write(json.dumps(page, ensure_ascii=False) + "\n")
    handle.flush()


# ----------------------------------------------------------------------------
# Сбор
# ----------------------------------------------------------------------------


def harvest(args):
    os.makedirs(CORPUS_DIR, exist_ok=True)
    pages, seen = load_cache(CACHE_PATH)
    print("в кэше уже %d статей" % len(pages))
    fetcher = Fetcher(pause=args.pause)
    added = 0
    with open(CACHE_PATH, "a", encoding="utf-8") as cache:
        # Вводные части: один вызов даёт двадцать статей. Дёшево и много.
        for i in range(args.batches):
            for page in random_intros(fetcher, args.intro_limit):
                if page["pageid"] in seen:
                    continue
                seen.add(page["pageid"])
                page["kind"] = "intro"
                pages.append(page)
                append_cache(cache, page)
                added += 1
            if (i + 1) % 20 == 0:
                print("  вводных частей: пачка %d из %d, всего статей %d"
                      % (i + 1, args.batches, len(pages)))
        # Полные статьи: тело статьи даёт прозу, а не только определение предмета.
        wanted = args.full_articles
        done = 0
        while done < wanted:
            candidates = random_pageids(fetcher, 10)
            if not candidates:
                break
            for page in candidates:
                if done >= wanted:
                    break
                if page["pageid"] in seen:
                    continue
                full = page if len(page["extract"]) > 1500 else full_article(fetcher, page["pageid"])
                if not full:
                    continue
                seen.add(full["pageid"])
                full["kind"] = "full"
                pages.append(full)
                append_cache(cache, full)
                added += 1
                done += 1
                if done % 50 == 0:
                    print("  полных статей: %d из %d, всего статей %d"
                          % (done, wanted, len(pages)))
    print("скачано новых статей: %d, сетевых вызовов: %d" % (added, fetcher.calls))
    return pages


def build(pages, args):
    """Собирает два набора из сырых статей."""
    random.seed(args.seed)
    negatives = []
    carriers = []
    neg_keys = set()
    car_keys = set()
    reasons = {}
    order = list(pages)
    random.shuffle(order)

    for page in order:
        person = is_person_article(page)
        sentences = []
        for paragraph in clean_lines(page.get("extract", "")):
            sentences.extend(split_sentences(paragraph))
        neg_here = 0
        car_here = 0
        for sentence in sentences:
            sentence = re.sub(r"\s+", " ", sentence).strip()
            if not sentence:
                continue
            key = normalize_key(sentence)
            if car_here < args.per_article_carriers and key not in car_keys and is_carrier(sentence):
                car_keys.add(key)
                carriers.append((page, sentence))
                car_here += 1
            if person or neg_here >= args.per_article_negatives:
                continue
            reason = negative_reason(sentence)
            if reason:
                reasons[reason] = reasons.get(reason, 0) + 1
                continue
            if key in neg_keys:
                continue
            neg_keys.add(key)
            negatives.append((page, sentence))
            neg_here += 1

    random.shuffle(negatives)
    random.shuffle(carriers)
    print("отобрано: отрицательных %d, носителей %d" % (len(negatives), len(carriers)))
    print("причины отказа в отрицательные (верх списка):")
    for reason, count in sorted(reasons.items(), key=lambda kv: -kv[1])[:12]:
        print("  %-32s %d" % (reason, count))

    neg_items = [
        make_item("wiki-neg-%06d" % i, "wiki_negative", text, [], False)
        for i, (_, text) in enumerate(negatives)
    ]
    car_items = [
        make_item("wiki-car-%06d" % i, "wiki_carrier", text, [], True)
        for i, (_, text) in enumerate(carriers)
    ]
    return neg_items, car_items


def write_jsonl(path, items):
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        for item in items:
            fh.write(json.dumps(item, ensure_ascii=False) + "\n")
    print("записано %d строк в %s" % (len(items), path))


def verify(path):
    """Перечитывает готовый файл и проверяет его так, как это сделает Go."""
    count = 0
    with open(path, "r", encoding="utf-8") as fh:
        for number, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line:
                continue
            item = json.loads(line)
            for field in ("id", "category", "source", "text"):
                if not item.get(field):
                    raise ValueError("%s строка %d: пустое поле %s" % (path, number, field))
            if not check_spans(item["text"], item["spans"]):
                raise ValueError("%s строка %d: смещения не сходятся" % (path, number))
            count += 1
    print("проверено %d строк в %s: разбор и смещения в порядке" % (count, path))
    return count


# ----------------------------------------------------------------------------
# Разбор ложных срабатываний
# ----------------------------------------------------------------------------


def analyze_false_positives(path, limit):
    """Показывает, на чём именно маскировщик спотыкается в настоящем тексте.

    Запускает go run ./cmd/score, разбирает его примеры «ЛИШНЕЕ» и вытаскивает
    из пары «текст - маска» сами изменённые куски. Без этого отчёт сообщает долю,
    но не говорит, какое правило её создало.
    """
    import subprocess

    cmd = ["go", "run", "./cmd/score", "-dataset", path, "-examples", str(limit)]
    proc = subprocess.run(cmd, capture_output=True)
    out = proc.stdout.decode("utf-8", "replace")
    if proc.returncode != 0:
        print(proc.stderr.decode("utf-8", "replace"), file=sys.stderr)
        return

    pairs = []
    lines = out.split("\n")
    for i, line in enumerate(lines):
        if not line.startswith("ЛИШНЕЕ"):
            continue
        if i + 2 >= len(lines):
            break
        src = lines[i + 1].strip()
        dst = lines[i + 2].strip()
        if not src.startswith("текст:") or not dst.startswith("маска:"):
            continue
        pairs.append((src[len("текст:"):].strip(), dst[len("маска:"):].strip()))

    counter = {}
    examples = []
    for src, dst in pairs:
        pieces = changed_pieces(src, dst)
        if not pieces:
            continue
        for piece in pieces:
            counter[piece] = counter.get(piece, 0) + 1
        examples.append((src, dst, pieces))

    print("\nложных срабатываний найдено в %d предложениях" % len(examples))
    print("самые частые лишние срабатывания:")
    for piece, count in sorted(counter.items(), key=lambda kv: (-kv[1], kv[0]))[:25]:
        print("  %4d  %s" % (count, piece))
    print("\nдесять примеров:")
    for src, dst, pieces in examples[:10]:
        print("  текст: %s" % src)
        print("  маска: %s" % dst)
        print("  съело: %s" % " | ".join(pieces))
        print()


def changed_pieces(src, dst):
    """Выделяет непрерывные куски, которые маска перекрыла."""
    a, b = list(src), list(dst)
    if len(a) != len(b):
        return []
    out = []
    i = 0
    while i < len(a):
        if a[i] == b[i]:
            i += 1
            continue
        j = i
        while j < len(a) and a[j] != b[j]:
            j += 1
        out.append("".join(a[i:j]))
        i = j
    return out


def main():
    parser = argparse.ArgumentParser(description="Сбор русского текста из Википедии")
    parser.add_argument("--batches", type=int, default=160,
                        help="сколько пачек вводных частей скачать, в каждой до двадцати статей")
    parser.add_argument("--intro-limit", type=int, default=20)
    parser.add_argument("--full-articles", type=int, default=500,
                        help="сколько статей скачать целиком")
    parser.add_argument("--pause", type=float, default=0.6,
                        help="пауза между сетевыми вызовами в секундах")
    parser.add_argument("--per-article-negatives", type=int, default=6)
    parser.add_argument("--per-article-carriers", type=int, default=10)
    parser.add_argument("--negative-target", type=int, default=3000)
    parser.add_argument("--carrier-target", type=int, default=5000)
    parser.add_argument("--sample-size", type=int, default=200)
    parser.add_argument("--seed", type=int, default=20260922)
    parser.add_argument("--build-only", action="store_true",
                        help="не ходить в сеть, собрать набор из кэша")
    parser.add_argument("--analyze", action="store_true",
                        help="после сборки разобрать ложные срабатывания через cmd/score")
    parser.add_argument("--analyze-limit", type=int, default=6000)
    args = parser.parse_args()

    if args.build_only:
        pages, _ = load_cache(CACHE_PATH)
        print("из кэша прочитано %d статей" % len(pages))
    else:
        pages = harvest(args)

    neg_items, car_items = build(pages, args)

    if len(neg_items) < args.negative_target:
        print("ВНИМАНИЕ: отрицательных %d, цель %d - нужен ещё сбор"
              % (len(neg_items), args.negative_target), file=sys.stderr)
    if len(car_items) < args.carrier_target:
        print("ВНИМАНИЕ: носителей %d, цель %d - нужен ещё сбор"
              % (len(car_items), args.carrier_target), file=sys.stderr)

    write_jsonl(NEGATIVE_PATH, neg_items)
    write_jsonl(CARRIER_PATH, car_items)
    verify(NEGATIVE_PATH)
    verify(CARRIER_PATH)

    os.makedirs(TESTDATA_DIR, exist_ok=True)
    write_jsonl(os.path.join(TESTDATA_DIR, "wiki_negative_sample.jsonl"),
                neg_items[: args.sample_size])
    write_jsonl(os.path.join(TESTDATA_DIR, "wiki_carrier_sample.jsonl"),
                car_items[: args.sample_size])

    if args.analyze:
        analyze_false_positives(NEGATIVE_PATH, args.analyze_limit)


if __name__ == "__main__":
    main()
