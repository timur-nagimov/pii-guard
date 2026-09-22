#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Сбор неформального русского текста для набора проверки качества.

Зачем. Проверяющая система подаёт не только аккуратные анкеты, но и живую
речь: обращения в поддержку, отзывы, переписку. В такой речи персональные
данные записаны небрежно, без ярлыков, вперемешку со смайлами, повторами
знаков и словами в верхнем регистре. Синтетический генератор такой стиль не
воспроизводит, поэтому фон для вставки берём из настоящих открытых наборов.

Что собирает.
  corpus/informal_carrier.jsonl   category informal_carrier, source reviews,
                                  partial_labels true, spans пустой. Носители
                                  для вставки персональных данных.
  corpus/informal_negative.jsonl  category informal_negative, source reviews,
                                  partial_labels false, spans пустой. Тексты,
                                  где персональных данных заведомо нет.

Источники, по убыванию предпочтения.
  1. d0rj/geo-reviews-dataset-2023 на huggingface.co. Это зеркало открытого
     набора Яндекса geo-reviews-dataset-2023 (500 000 отзывов об организациях
     на Яндекс Картах, лицензия MIT). В самом репозитории github.com/yandex/
     geo-reviews-dataset-2023 лежит только README без файла данных и без
     релизов, поэтому берём зеркало.
  2. Romjiik/Russian_bank_reviews на huggingface.co. 12 392 отзыва и жалобы
     на банки, ровно тот жанр, который нас интересует.
  3. Raven-SL/ru-pnames-list на github.com. Списки русских имён и фамилий.
     Нужны для отбора отрицательных элементов: текст, где встретилось любое
     словарное имя или фамилия, в отрицательные не попадает.
  4. Если сеть недоступна, собственный комбинаторный генератор разговорных
     обращений в банк (не менее тысячи разных шаблонов) с опечатками,
     сокращениями, смайлами и повторами знаков. Такие элементы получают
     source synthetic, чтобы не выдавать свою выдумку за настоящие данные.

Смещения. Все собранные элементы идут с пустым spans, поэтому перевод
смещений из символов в байты здесь не нужен. Тем не менее файл проверяется
функцией verify_offsets: она разбирает каждую строку так же, как это сделает
Go, и убеждается, что байтовые границы фрагментов ложатся на границы рун.

Запуск:
    python3 tools/fetch_informal.py
    python3 tools/fetch_informal.py --carrier-target 3000 --negative-target 500
"""

import argparse
import hashlib
import json
import os
import random
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CACHE_DIR = os.path.join(ROOT, "corpus", "_cache")
CARRIER_PATH = os.path.join(ROOT, "corpus", "informal_carrier.jsonl")
NEGATIVE_PATH = os.path.join(ROOT, "corpus", "informal_negative.jsonl")
TESTDATA_DIR = os.path.join(ROOT, "testdata")

# Источники на huggingface.co: имя набора и поле с текстом отзыва.
HF_SOURCES = [
    ("d0rj/geo-reviews-dataset-2023", "default", "train", "text", "geo"),
    ("Romjiik/Russian_bank_reviews", "default", "train", "review", "bank"),
]

# Словари имён и фамилий: ими отсеиваем отрицательные элементы, в которых
# на самом деле есть имя человека. Без словаря такой текст выглядит чистым,
# потому что имя стоит в начале предложения и от обычного слова неотличимо.
NAME_LISTS = [
    "https://raw.githubusercontent.com/Raven-SL/ru-pnames-list/master/lists/female_names_rus.txt",
    "https://raw.githubusercontent.com/Raven-SL/ru-pnames-list/master/lists/male_names_rus.txt",
    "https://raw.githubusercontent.com/Raven-SL/ru-pnames-list/master/lists/male_surnames_rus.txt",
]

# Сколько отзывов поднимаем из каждого источника. Берём с запасом: строгий
# отбор отрицательных отсеивает подавляющее большинство кандидатов.
POOL_LIMIT = 120000

# Общий словарь русских словоформ. Слово с заглавной, которого в нём нет,
# почти наверняка имя собственное: имя, фамилия, город или бренд.
WORDS_URL = "https://raw.githubusercontent.com/danakt/russian-words/master/russian.txt"

MIN_CHARS = 40
MAX_CHARS = 300
SEED = 20240517


# ---------------------------------------------------------------------------
# Загрузка
# ---------------------------------------------------------------------------


def http_get(url, retries=4, timeout=120):
    """Читает URL с повторами: сеть на чужой машине бывает капризной."""
    last = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "pii-guard/1.0"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.read()
        except (urllib.error.URLError, OSError) as err:
            last = err
            time.sleep(2 * (attempt + 1))
    raise RuntimeError("не удалось скачать %s: %s" % (url, last))


def cached_text(url, name, encodings=("utf-8", "cp1251")):
    """Скачивает текстовый файл в кеш и возвращает его строки.

    Словари имён и словоформ весят десятки мегабайт, качать их при каждом
    запуске незачем. Кодировку подбираем: часть открытых списков лежит в
    cp1251, а не в utf-8.
    """
    os.makedirs(CACHE_DIR, exist_ok=True)
    dest = os.path.join(CACHE_DIR, name)
    if not os.path.exists(dest) or os.path.getsize(dest) == 0:
        data = http_get(url, timeout=600)
        tmp = dest + ".part"
        with open(tmp, "wb") as fh:
            fh.write(data)
        os.replace(tmp, dest)
    with open(dest, "rb") as fh:
        raw = fh.read()
    for enc in encodings:
        try:
            return raw.decode(enc).splitlines()
        except UnicodeDecodeError:
            continue
    return raw.decode("utf-8", "replace").splitlines()


def download_parquet(dataset, config, split, tag):
    """Скачивает parquet-файлы набора в кеш и возвращает пути к ним.

    Кеш лежит в corpus/_cache, а каталог corpus целиком в .gitignore, так что
    в репозиторий сырые данные не попадают.
    """
    os.makedirs(CACHE_DIR, exist_ok=True)
    listing_url = "https://huggingface.co/api/datasets/%s/parquet/%s/%s" % (
        urllib.parse.quote(dataset),
        config,
        split,
    )
    urls = json.loads(http_get(listing_url, timeout=60).decode("utf-8"))
    paths = []
    for i, url in enumerate(urls):
        dest = os.path.join(CACHE_DIR, "%s-%d.parquet" % (tag, i))
        if not os.path.exists(dest) or os.path.getsize(dest) == 0:
            data = http_get(url, timeout=900)
            tmp = dest + ".part"
            with open(tmp, "wb") as fh:
                fh.write(data)
            os.replace(tmp, dest)
        paths.append(dest)
    return paths


def read_parquet_column(paths, column, limit):
    """Читает одну колонку из parquet, беря понемногу из каждой группы строк.

    Целиком набор в память не поднимаем: в geo-reviews полмиллиона отзывов.
    Вместо этого проходим по группам строк и из каждой берём равную долю, так
    разнообразие по рубрикам и городам сохраняется, а память остаётся мелкой.
    """
    import pyarrow.parquet as pq  # локальный импорт: без сети он может отсутствовать

    out = []
    for path in paths:
        handle = pq.ParquetFile(path)
        groups = handle.num_row_groups
        if groups == 0:
            continue
        per_group = max(1, (limit - len(out)) // groups + 1)
        for gi in range(groups):
            if len(out) >= limit:
                break
            table = handle.read_row_group(gi, columns=[column])
            values = table.column(column).to_pylist()
            step = max(1, len(values) // per_group)
            for value in values[::step]:
                if value:
                    out.append(value)
                if len(out) >= limit:
                    break
    return out


def read_rows_api(dataset, config, split, column, limit):
    """Запасной путь без pyarrow: постраничное чтение через datasets-server.

    Ручка отдаёт максимум сто строк за раз, поэтому идём окнами и равномерно
    разбрасываем смещения по всему набору ради разнообразия.
    """
    base = "https://datasets-server.huggingface.co/rows?dataset=%s&config=%s&split=%s" % (
        urllib.parse.quote(dataset, safe=""),
        config,
        split,
    )
    head = json.loads(http_get(base + "&offset=0&length=1", timeout=60).decode("utf-8"))
    total = int(head.get("num_rows_total") or 0)
    if total <= 0:
        return []
    window = 100
    windows = max(1, min(total // window, (limit + window - 1) // window))
    step = max(1, total // windows)
    out = []
    for i in range(windows):
        offset = min(i * step, max(0, total - window))
        url = "%s&offset=%d&length=%d" % (base, offset, window)
        try:
            page = json.loads(http_get(url, timeout=90).decode("utf-8"))
        except RuntimeError:
            continue
        for row in page.get("rows", []):
            value = row.get("row", {}).get(column)
            if value:
                out.append(value)
        if len(out) >= limit:
            break
    return out


def load_source(dataset, config, split, column, tag, limit):
    """Достаёт тексты набора: сначала parquet, затем построчная ручка."""
    try:
        paths = download_parquet(dataset, config, split, tag)
        texts = read_parquet_column(paths, column, limit)
        if texts:
            return texts, "parquet"
    except Exception as err:  # noqa: BLE001 - любой сбой означает переход к запасному пути
        sys.stderr.write("parquet для %s не вышел (%s), пробую построчную ручку\n" % (dataset, err))
    try:
        texts = read_rows_api(dataset, config, split, column, limit)
        if texts:
            return texts, "rows-api"
    except Exception as err:  # noqa: BLE001
        sys.stderr.write("построчная ручка для %s не вышла (%s)\n" % (dataset, err))
    return [], "нет"


# ---------------------------------------------------------------------------
# Нарезка и нормализация
# ---------------------------------------------------------------------------

WS_RE = re.compile(r"[\s   ]+")
SENT_SPLIT_RE = re.compile(r"(?<=[.!?…])\s+")
CYRILLIC_RE = re.compile(r"[а-яёА-ЯЁ]")


def normalize(text):
    """Схлопывает переводы строк и лишние пробелы, сохраняя знаки и смайлы."""
    text = text.replace("\\n", " ").replace("​", " ")
    text = WS_RE.sub(" ", text)
    return text.strip()


def split_fragments(text):
    """Режет отзыв на куски длиной от сорока до трёхсот символов.

    Склеиваем предложения подряд, пока помещаемся в верхнюю границу. Куски
    короче нижней границы выбрасываем: слишком короткий носитель бесполезен.
    """
    text = normalize(text)
    if not text:
        return []
    pieces = [p for p in SENT_SPLIT_RE.split(text) if p]
    out = []
    buf = ""
    for piece in pieces:
        if len(piece) > MAX_CHARS:
            # Одно предложение длиннее верхней границы: режем по словам.
            if buf:
                out.append(buf)
                buf = ""
            words = piece.split(" ")
            chunk = ""
            for word in words:
                if len(chunk) + len(word) + 1 > MAX_CHARS:
                    if chunk:
                        out.append(chunk)
                    chunk = word[:MAX_CHARS]
                else:
                    chunk = (chunk + " " + word).strip()
            if chunk:
                out.append(chunk)
            continue
        candidate = (buf + " " + piece).strip() if buf else piece
        if len(candidate) > MAX_CHARS:
            out.append(buf)
            buf = piece
        else:
            buf = candidate
    if buf:
        out.append(buf)
    return [f for f in (x.strip() for x in out) if MIN_CHARS <= len(f) <= MAX_CHARS]


def usable_carrier(text):
    """Отсеивает мусор: без кириллицы, из одних знаков, слишком мало слов."""
    if not (MIN_CHARS <= len(text) <= MAX_CHARS):
        return False
    letters = len(CYRILLIC_RE.findall(text))
    if letters < len(text) * 0.45:
        return False
    if len(text.split(" ")) < 6:
        return False
    return True


# ---------------------------------------------------------------------------
# Отбор отрицательных элементов
# ---------------------------------------------------------------------------

PATRONYMIC_RE = re.compile(r"[а-яё](?:ович|евич|ьич|овна|евна|ична|инична)\b", re.IGNORECASE)
CAP_WORD_RE = re.compile(r"[А-ЯЁ][а-яё]{2,}")
# Одиночная заглавная с точкой это инициал, а заглавная с дефисом и
# строчным хвостом это затёртая фамилия вида «Я-ой». И то и другое ссылается
# на конкретного человека, поэтому в отрицательный срез не годится.
INITIALS_RE = re.compile(r"(?:[А-ЯЁ]\s*\.)|(?:\b[А-ЯЁ]-[а-яё])")
ADDRESS_RE = re.compile(
    r"(?:\b(?:ул|г|д|кв|пр|пр-т|пер|обл|стр|корп|мкр|просп|бул|наб|ш|пл|р-н)\s*\.)"
    r"|(?:\b(?:улиц\w*|город\w*|проспект\w*|переул\w*|шоссе|квартир\w*|индекс\w*|"
    r"бульвар\w*|набережн\w*|микрорайон\w*|посёл\w*|посел\w*|станиц\w*|деревн\w*|"
    r"область|области|район\w*|край|края|республик\w*|паспорт\w*|снилс|инн|"
    r"телефон\w*|почт\w*|адрес\w*|карт\w*|счёт|счет\w*|родил\w*)\b)",
    re.IGNORECASE,
)
MONTH_RE = re.compile(
    r"\b(январ|феврал|март|апрел|мая|май|июн|июл|август|сентябр|октябр|ноябр|декабр)",
    re.IGNORECASE,
)


# Списки Raven-SL почти целиком славянские, поэтому дополняем их частыми
# именами народов России и СНГ: без этого «Ахмед замечательно постриг»
# проходит в отрицательный срез, хотя имя там настоящее.
EXTRA_NAMES = """
ахмед ахмет азамат азат айдар айгуль айна айрат алан алим алия альбина амир
анвар арман арсен артур аслан ахмад бахтиёр бекзат булат вагиф вахид гулнара
гульнара гульнур дамир даниял диана динара джамал джамиля дильшод егана
елдар жанна закир заур ибрагим идрис ильгиз ильдар ильнур ильхам инга ирек
искандер ислам исмаил кадыр камиль карим касым кемал лейла ленар лиана линар
магомед мадина мансур марат махмуд мурад мурат муса мустафа нариман нурлан
нурсултан олжас ойбек рамазан рамиль рашид ренат рифат рустам рустем руслан
саид салават салим самир сабина сафия сулейман тагир таир тамерлан тимур
тахир фарид фаиль фируза хамид хасан шамиль шахин эдгар эльвира эльдар эльмира
эмиль эркин юнус юсуф ясмина
"""


# Словарь имён и фамилий заполняется один раз при старте.
NAME_VOCAB = set()
WORD_VOCAB = set()


def fold(word):
    """Приводит слово к виду для сравнения со словарём: нижний регистр, е вместо ё."""
    return word.lower().replace("\u0451", "\u0435")


def load_name_vocab():
    """Качает списки имён и фамилий и раскрывает женские формы фамилий.

    Списки мужские, поэтому к каждой фамилии добавляем женскую пару: Иванов
    даёт Иванова, Синицкий даёт Синицкая, Кузьмин даёт Кузьмина. Иначе
    отрицательные элементы протекут на строчках вида «Петрова помогла».
    """
    vocab = set(EXTRA_NAMES.split())
    for url in NAME_LISTS:
        try:
            lines = cached_text(url, "names-" + url.rsplit("/", 1)[-1])
        except RuntimeError as err:
            sys.stderr.write("словарь имён %s не скачался (%s)\n" % (url, err))
            continue
        for line in lines:
            word = line.strip()
            if len(word) < 3:
                continue
            key = fold(word)
            vocab.add(key)
            if key.endswith(("ов", "ев", "ин", "ын")):
                vocab.add(key + "а")
            elif key.endswith("ский") or key.endswith("цкий"):
                vocab.add(key[:-2] + "ая")
            elif key.endswith("ой") or key.endswith("ый") or key.endswith("ий"):
                vocab.add(key[:-2] + "ая")
    return vocab


def load_word_vocab():
    """Качает общий словарь русских словоформ (около полутора миллионов форм)."""
    try:
        lines = cached_text(WORDS_URL, "russian-words.txt")
    except RuntimeError as err:
        sys.stderr.write("общий словарь слов не скачался (%s)\n" % err)
        return set()
    vocab = set()
    for line in lines:
        word = line.strip()
        if len(word) >= 3:
            vocab.add(fold(word))
    return vocab


def looks_like_name(word):
    """Слово похоже на имя или фамилию с учётом падежных окончаний.

    Словари дают только именительный падеж, а в живой речи пишут «спасибо
    Ивану», «благодарю Наталью». Поэтому проверяем не только само слово, но и
    его укороченные основы.
    """
    key = fold(word)
    if key in NAME_VOCAB:
        return True
    for cut_len in (1, 2, 3):
        stem = key[:-cut_len]
        if len(stem) >= 4 and stem in NAME_VOCAB:
            return True
    return False


def has_known_name(text):
    """Есть ли в тексте слово из словаря имён и фамилий."""
    if not NAME_VOCAB:
        return False
    for match in re.finditer(r"[А-ЯЁ][а-яё]+", text):
        if looks_like_name(match.group(0)):
            return True
    return False


def has_unknown_capitalized(text):
    """Есть ли слово с заглавной, которого нет в общем словаре русского языка.

    Такое слово почти всегда имя собственное: редкое имя, фамилия, город или
    название заведения. Для отрицательного среза это повод отказаться.
    """
    if not WORD_VOCAB:
        return False
    for match in re.finditer(r"[А-ЯЁ][а-яё]+", text):
        if fold(match.group(0)) not in WORD_VOCAB:
            return True
    return False


def capitalized_outside_sentence_start(text):
    """Ищет слово с заглавной, стоящее не в начале предложения.

    Такое слово почти всегда имя, фамилия, название организации или города,
    то есть ровно то, за что зацепится детектор. Для отрицательных элементов
    это недопустимо: там любое изменение считается ошибкой.
    """
    # Позиции, с которых начинается предложение: начало текста и позиция после
    # завершающего знака с пробелом.
    starts = {0}
    for match in re.finditer(r"[.!?…]+[\s\"'«»(]*", text):
        starts.add(match.end())
    for match in CAP_WORD_RE.finditer(text):
        if match.start() not in starts:
            return True
    return False


def is_clean_negative(text):
    """Строгие эвристики: в тексте заведомо нет персональных данных."""
    if not usable_carrier(text):
        return False
    if "@" in text or "#" in text or "№" in text:
        return False
    if re.search(r"https?://|www\.|\.ru\b|\.com\b|\.рф\b", text, re.IGNORECASE):
        return False
    if re.search(r"[A-Za-z]", text):
        # Латиница почти всегда означает бренд, модель или набор цифробукв,
        # которые детектор принимает за документ или карту.
        return False
    if re.search(r"\d", text):
        # Полностью запрещаем цифры: любое число может сойти за дату, индекс,
        # код подразделения или обрывок номера.
        return False
    if PATRONYMIC_RE.search(text):
        return False
    if INITIALS_RE.search(text):
        return False
    if ADDRESS_RE.search(text):
        return False
    if MONTH_RE.search(text):
        return False
    if capitalized_outside_sentence_start(text):
        return False
    if has_known_name(text):
        # Имя или фамилия из словаря: это настоящие персональные данные,
        # в отрицательный срез такой текст класть нельзя.
        return False
    if has_unknown_capitalized(text):
        # Слова с заглавной нет в общем словаре: скорее всего имя собственное.
        return False
    if re.search(r"\b[А-ЯЁ]{2,}\b", text):
        # Слово целиком в верхнем регистре тоже читается детектором как имя.
        return False
    return True


# ---------------------------------------------------------------------------
# Запасной комбинаторный генератор
# ---------------------------------------------------------------------------

OPENINGS = [
    "здравствуйте", "добрый день", "доброе утро", "здрасте", "приветствую",
    "добрый вечер", "здравствуйте уважаемые", "ну здравствуйте", "привет",
    "доброго времени суток", "день добрый", "добрый",
]
COMPLAINTS = [
    "уже третий день не могу зайти в приложение",
    "списали деньги дважды за одну покупку",
    "карту заблокировали без предупреждения",
    "перевод висит в обработке вторые сутки",
    "в отделении сказали ждать неделю",
    "поддержка не отвечает вообще никак",
    "кэшбэк не начислили за прошлый месяц",
    "смс с кодом не приходит совсем",
    "очередь была на полтора часа",
    "менеджер обещал перезвонить и пропал",
    "комиссию сняли молча и без объяснений",
    "в чате бот гоняет по кругу",
    "заявку отклонили без причины",
    "вклад закрыли раньше срока непонятно почему",
    "приложение вылетает при входе",
    "банкомат зажевал купюры и чек не выдал",
    "лимит снизили и никто не предупредил",
    "документы потеряли уже второй раз",
    "выписку жду с прошлой недели",
    "страховку навязали при оформлении",
]
DETAILS = [
    "деньги нужны срочно",
    "это вообще нормально",
    "сколько можно ждать",
    "разберитесь пожалуйста",
    "очень прошу помочь",
    "я в шоке честно говоря",
    "никому не советую такое",
    "терпение уже кончается",
    "хочу понять что делать дальше",
    "жду внятного ответа",
    "подскажите куда писать",
    "верните всё как было",
]
CLOSINGS = [
    "спасибо заранее", "жду ответа", "с уважением", "заранее благодарю",
    "надеюсь на понимание", "прошу решить вопрос", "очень жду", "спс",
]
EMOJI = ["", "", "", " :(", " )))", " ((", " :-(", " 😡", " 😢", " 🙏", " !!!", "…"]
NOISE = [
    ("что", "што"), ("сейчас", "щас"), ("пожалуйста", "пжлст"),
    ("нормально", "нармально"), ("вообще", "ваще"), ("который", "каторый"),
]


def synthetic_informal(count, rnd):
    """Комбинаторно порождает разговорные обращения в банк.

    Используется, только если ни один открытый источник не отозвался. Даёт
    заведомо больше тысячи разных шаблонов: 12 * 20 * 12 * 8 вариантов основы
    плюс шум из опечаток, верхнего регистра и повторов знаков.
    """
    seen = set()
    out = []
    guard = 0
    while len(out) < count and guard < count * 50:
        guard += 1
        parts = [
            rnd.choice(OPENINGS),
            rnd.choice(COMPLAINTS),
            rnd.choice(DETAILS),
            rnd.choice(CLOSINGS),
        ]
        text = ", ".join(parts) + rnd.choice(EMOJI)
        if rnd.random() < 0.4:
            src, dst = rnd.choice(NOISE)
            text = text.replace(src, dst)
        if rnd.random() < 0.25:
            words = text.split(" ")
            i = rnd.randrange(len(words))
            words[i] = words[i].upper()
            text = " ".join(words)
        if rnd.random() < 0.2:
            text = text.replace(".", "...")
        text = normalize(text)
        if not (MIN_CHARS <= len(text) <= MAX_CHARS):
            continue
        key = text.lower()
        if key in seen:
            continue
        seen.add(key)
        out.append(text)
    return out


# ---------------------------------------------------------------------------
# Сборка набора
# ---------------------------------------------------------------------------


def dedup_key(text):
    """Ключ для отсева повторов: регистр и знаки не учитываем."""
    flat = re.sub(r"[^а-яёa-z0-9 ]+", "", text.lower())
    flat = WS_RE.sub(" ", flat).strip()
    return hashlib.sha1(flat.encode("utf-8")).hexdigest()


def build(carrier_target, negative_target, per_review_cap):
    global NAME_VOCAB, WORD_VOCAB  # noqa: PLW0603 - словари одни на весь запуск
    NAME_VOCAB = load_name_vocab()
    WORD_VOCAB = load_word_vocab()
    sys.stderr.write(
        "словарь имён и фамилий: %d форм, общий словарь: %d форм\n"
        % (len(NAME_VOCAB), len(WORD_VOCAB))
    )
    rnd = random.Random(SEED)
    pools = {}
    report = {}
    for dataset, config, split, column, tag in HF_SOURCES:
        texts, how = load_source(dataset, config, split, column, tag, limit=POOL_LIMIT)
        report[dataset] = (len(texts), how)
        sys.stderr.write("%s: %d отзывов через %s\n" % (dataset, len(texts), how))
        if texts:
            rnd.shuffle(texts)
            pools[tag] = texts

    carriers = []
    negatives = []
    seen = set()

    if pools:
        # Идём по источникам по очереди, чтобы оба жанра были представлены.
        cursors = {tag: 0 for tag in pools}
        order = sorted(pools)
        done = set()
        while len(done) < len(order) and (
            len(carriers) < carrier_target * 2 or len(negatives) < negative_target * 3
        ):
            for tag in order:
                if tag in done:
                    continue
                texts = pools[tag]
                idx = cursors[tag]
                if idx >= len(texts):
                    done.add(tag)
                    continue
                cursors[tag] = idx + 1
                taken = 0
                for frag in split_fragments(texts[idx]):
                    if taken >= per_review_cap:
                        break
                    if not usable_carrier(frag):
                        continue
                    key = dedup_key(frag)
                    if key in seen:
                        continue
                    seen.add(key)
                    taken += 1
                    if is_clean_negative(frag) and len(negatives) < negative_target * 3:
                        negatives.append((tag, frag))
                    else:
                        carriers.append((tag, frag))
            if len(carriers) >= carrier_target * 2 and len(negatives) >= negative_target * 3:
                break

    fallback_used = False
    if len(carriers) < carrier_target or len(negatives) < negative_target:
        # Открытые источники не дали нужного объёма: добираем своим генератором.
        fallback_used = True
        need = max(carrier_target - len(carriers), 0) + max(negative_target - len(negatives), 0)
        for text in synthetic_informal(need + 200, rnd):
            key = dedup_key(text)
            if key in seen:
                continue
            seen.add(key)
            if is_clean_negative(text) and len(negatives) < negative_target:
                negatives.append(("synthetic", text))
            elif len(carriers) < carrier_target:
                carriers.append(("synthetic", text))

    rnd.shuffle(carriers)
    rnd.shuffle(negatives)
    if carrier_target:
        carriers = carriers[:carrier_target]
    if negative_target:
        negatives = negatives[:negative_target]
    return carriers, negatives, report, fallback_used


def to_items(rows, prefix, category, partial):
    """Оборачивает тексты в элементы набора единого формата."""
    items = []
    for i, (tag, text) in enumerate(rows):
        items.append(
            {
                "id": "%s-%06d" % (prefix, i),
                "category": category,
                "source": "synthetic" if tag == "synthetic" else "reviews",
                "text": text,
                "spans": [],
                "partial_labels": partial,
            }
        )
    return items


def write_jsonl(path, items):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        for item in items:
            fh.write(json.dumps(item, ensure_ascii=False) + "\n")


def verify_offsets(path):
    """Проверяет файл так, как его прочитает Go.

    Каждая строка обязана разбираться как JSON, текст обязан быть корректным
    UTF-8, а границы фрагментов обязаны попадать на границы рун и вырезать
    ровно тот кусок, который заявлен. Для пустых spans проверка тривиальна,
    но она страхует от ошибки, если поле когда-нибудь начнут заполнять.
    """
    bad = 0
    total = 0
    with open(path, "r", encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.strip()
            if not line:
                continue
            total += 1
            item = json.loads(line)
            raw = item["text"].encode("utf-8")
            for span in item.get("spans") or []:
                start, end = span["start"], span["end"]
                if not (0 <= start < end <= len(raw)):
                    sys.stderr.write("строка %d: смещения вне текста\n" % lineno)
                    bad += 1
                    continue
                try:
                    raw[start:end].decode("utf-8")
                except UnicodeDecodeError:
                    sys.stderr.write("строка %d: смещения режут руну пополам\n" % lineno)
                    bad += 1
    return total, bad


SELFTEST_VALUES = [
    ("FIO", "Кузьмина Анна Олеговна"),
    ("PHONE", "+7 (926) 431-93-16"),
    ("EMAIL", "anna.kuzmina@mail.ru"),
    ("SNILS", "321 323 990 34"),
    ("INN", "487547385831"),
    ("CARD", "4276 3800 1234 5678"),
]


def selftest(limit=300):
    """Проверяет байтовую арифметику смещений на собранных носителях.

    Сами элементы идут с пустым spans, поэтому ошибиться негде. Но носители
    отдаются соседнему шагу, который вставляет в них значения и обязан
    посчитать границы в байтах, а не в символах. Здесь мы показываем, как это
    считается правильно, и убеждаемся, что срез байтов даёт ровно вставленное
    значение. Кириллическая буква занимает два байта, поэтому позиция в
    символах и позиция в байтах расходятся почти всегда.
    """
    if not os.path.exists(CARRIER_PATH):
        sys.stderr.write("сначала соберите носители\n")
        return 1
    rnd = random.Random(SEED + 1)
    checked = 0
    mismatched = 0
    shifted = 0
    with open(CARRIER_PATH, "r", encoding="utf-8") as fh:
        for lineno, line in enumerate(fh):
            if checked >= limit:
                break
            item = json.loads(line)
            text = item["text"]
            kind, value = SELFTEST_VALUES[lineno % len(SELFTEST_VALUES)]
            # Вставляем значение на границе слова, в середине текста.
            words = text.split(" ")
            cut = rnd.randrange(1, max(2, len(words)))
            prefix = " ".join(words[:cut]) + " "
            merged = prefix + value + " " + " ".join(words[cut:])
            # Смещения считаем в байтах: длина префикса в кодировке utf-8.
            start = len(prefix.encode("utf-8"))
            end = start + len(value.encode("utf-8"))
            raw = merged.encode("utf-8")
            checked += 1
            if raw[start:end].decode("utf-8") != value:
                mismatched += 1
                sys.stderr.write("строка %d: срез байтов не совпал с %s\n" % (lineno, kind))
            if start != len(prefix):
                # Символьное смещение отличается от байтового: именно этот
                # случай и ломает наивный перенос смещений из Python в Go.
                shifted += 1
    print(
        "самопроверка смещений: %d вставок, расхождений %d, "
        "смещение в байтах отличалось от смещения в символах в %d случаях"
        % (checked, mismatched, shifted)
    )
    return 1 if mismatched else 0


def main():
    parser = argparse.ArgumentParser(description="сбор неформального русского текста")
    parser.add_argument("--carrier-target", type=int, default=6000)
    parser.add_argument("--negative-target", type=int, default=1500)
    parser.add_argument("--per-review-cap", type=int, default=2)
    parser.add_argument("--sample-size", type=int, default=200)
    parser.add_argument(
        "--selftest",
        action="store_true",
        help="только проверить байтовую арифметику смещений на готовых носителях",
    )
    args = parser.parse_args()

    if args.selftest:
        sys.exit(selftest())

    carriers, negatives, report, fallback = build(
        args.carrier_target, args.negative_target, args.per_review_cap
    )

    carrier_items = to_items(carriers, "inf", "informal_carrier", True)
    negative_items = to_items(negatives, "infneg", "informal_negative", False)

    write_jsonl(CARRIER_PATH, carrier_items)
    write_jsonl(NEGATIVE_PATH, negative_items)
    os.makedirs(TESTDATA_DIR, exist_ok=True)
    write_jsonl(
        os.path.join(TESTDATA_DIR, "informal_carrier.jsonl"),
        carrier_items[: args.sample_size],
    )
    write_jsonl(
        os.path.join(TESTDATA_DIR, "informal_negative.jsonl"),
        negative_items[: args.sample_size],
    )

    for path in (CARRIER_PATH, NEGATIVE_PATH):
        total, bad = verify_offsets(path)
        print("%s: %d элементов, ошибок смещений %d" % (path, total, bad))
    for dataset, (count, how) in sorted(report.items()):
        print("источник %s: %d отзывов, способ %s" % (dataset, count, how))
    if fallback:
        print("часть элементов добрана комбинаторным генератором (source synthetic)")


if __name__ == "__main__":
    main()
