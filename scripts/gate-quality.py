#!/usr/bin/env python3
"""Сравнение качества с базовой линией.

Читает вывод cmd/score и файл базовой линии, печатает расхождения и возвращает
единицу, если стало хуже.

Правил два, и они разной строгости.

Положительные срезы сравниваются с допуском: замер шумит на сотые доли,
и придираться к четвёртому знаку значит закрывать ворота на ровном месте.
Допуск задаётся в самой базовой линии, по умолчанию 0.005.

Отрицательные срезы, имена которых начинаются на neg_, сравниваются по доле
ложных срабатываний — колонке «ложных» в выводе измерителя.

Почему не по колонке «изменено», как было раньше. В отрицательном срезе нет
размеченных фрагментов, а «изменено» считается как changed/fragments, и при
нулевом знаменателе измеритель возвращает ноль (cmd/score, функция avg).
То есть прежняя проверка «строго ноль» сравнивала с нулём величину, которая
равна нулю по построению, и не могла сработать никогда. При этом настоящие
ложные срабатывания в тех же срезах доходили до 30 процентов и ворота их
не видели.

Проверка ворот это не поймала по той же причине: подложное значение в
gate-selfcheck вписывалось в колонку «изменено», которой реальный замер
для этих срезов не заполняет. Проверка проверки оказалась такой же
бессмысленной, как сама проверка.

Сравнение с базовой линией, а не с нулём: часть ложных срабатываний
известна и осознанно принята, ноль означал бы закрытые ворота с первой же
минуты. Ворота ловят рост — то есть новую поломку, а не старую.

Использование:
    gate-quality.py baseline.json score-output.txt
    gate-quality.py --init score-output.txt > baseline.json
"""

import json
import re
import sys

# Строка вывода измерителя, семь колонок:
#   срез  изменено  затронуто%  пропущено%  частично%  ложных%  фрагментов
#
# Колонка «ложных» необязательна: измеритель печатает её пустой, когда за
# пределами размеченных фрагментов текста не было. Раньше выражение
# останавливалось на «затронуто» и брало число фрагментов из следующей
# колонки — то есть из доли пропущенных. Значение было мусорным у всех строк,
# просто никто на него не смотрел.
ROW = re.compile(
    r"^(?P<name>[A-Za-zА-Яа-я_][\w./-]*)\s+"
    r"(?P<score>\d+\.\d+)\s+"
    r"(?P<touched>[\d.]+)%\s+"
    r"(?P<missed>[\d.]+)%\s+"
    r"(?P<partial>[\d.]+)%\s+"
    r"(?:(?P<extra>[\d.]+)%\s+)?"
    r"(?P<fragments>\d+)"
)

DEFAULT_TOLERANCE = 0.005

# Допуск для доли ложных срабатываний, в процентных пунктах. Замер на
# отрицательных срезах шумит слабее, чем на положительных, но нулевой допуск
# закрывал бы ворота на дрожании последнего знака.
DEFAULT_FP_TOLERANCE = 0.5


def parse(path):
    """Достаёт из вывода измерителя таблицу по срезам."""
    rows = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line or line.startswith(("срез", "ПРОПУСК", "ЛИШНЕЕ", " ")):
                continue
            m = ROW.match(line)
            if not m:
                continue
            rows[m.group("name")] = {
                "score": float(m.group("score")),
                "touched": float(m.group("touched")),
                "missed": float(m.group("missed")),
                "partial": float(m.group("partial")),
                "extra": float(m.group("extra")) if m.group("extra") else 0.0,
                "fragments": int(m.group("fragments")),
            }
    return rows


def init_baseline(path):
    """Снимает базовую линию с вывода измерителя."""
    rows = parse(path)
    if not rows:
        print("не разобрано ни одной строки", file=sys.stderr)
        return 2
    out = {
        "_комментарий": (
            "Базовая линия качества. Меняет только человек и только "
            "осознанно: ворота сравнивают с этими числами. Если их "
            "может подвинуть тот, кто правит код, ворота бесполезны."
        ),
        "_допуск": DEFAULT_TOLERANCE,
        "_допуск_ложных": DEFAULT_FP_TOLERANCE,
        "срезы": {k: v["score"] for k, v in sorted(rows.items())
                  if not k.startswith("neg_")},
        # Отрицательные срезы закрепляются по доле ложных срабатываний,
        # а не по доле изменённого: у них нет размеченных фрагментов,
        # и «изменено» равно нулю по построению.
        "ложные": {k: v["extra"] for k, v in sorted(rows.items())
                   if k.startswith("neg_")},
    }
    print(json.dumps(out, ensure_ascii=False, indent=2))
    return 0


def check_negative(name, fp, base_fp, tolerance):
    """Отрицательный срез: сравнивает долю ложных срабатываний с линией.

    Возвращает пару «стало хуже, стало лучше»; обе половины необязательны,
    потому что расхождение внутри допуска это не новость ни в одну сторону.
    """
    if name not in base_fp:
        print(f"новый отрицательный срез {name}: ложных {fp:.2f}%, "
              f"в базовой линии его нет", file=sys.stderr)
        return None, None

    grew = fp - base_fp[name]
    if grew > tolerance:
        return (f"{name}: ложных срабатываний было {base_fp[name]:.2f}%, "
                f"стало {fp:.2f}%, рост {grew:.2f} при допуске {tolerance}"), None
    if grew < -tolerance:
        return None, (f"{name}: ложных {base_fp[name]:.2f}% → {fp:.2f}% "
                      f"({-grew:.2f} меньше)")
    return None, None


def check_positive(name, score, base, tolerance):
    """Положительный срез: сравнивает оценку с линией."""
    if name not in base:
        # Новый срез это не повод закрывать ворота, но человек должен
        # знать, что базовая линия отстала.
        print(f"новый срез {name}: {score:.4f}, в базовой линии его нет",
              file=sys.stderr)
        return None, None

    delta = score - base[name]
    if delta < -tolerance:
        return (f"{name}: было {base[name]:.4f}, стало {score:.4f}, "
                f"просадка {-delta:.4f} при допуске {tolerance}"), None
    if delta > tolerance:
        return None, f"{name}: {base[name]:.4f} → {score:.4f} (+{delta:.4f})"
    return None, None


def compare(rows, baseline):
    """Сводит замер с базовой линией: что стало хуже и что стало лучше."""
    base = baseline.get("срезы", {})
    base_fp = baseline.get("ложные", {})
    tolerance = float(baseline.get("_допуск", DEFAULT_TOLERANCE))
    fp_tolerance = float(baseline.get("_допуск_ложных", DEFAULT_FP_TOLERANCE))

    problems = []
    improved = []

    for name, cur in sorted(rows.items()):
        if name.startswith("neg_"):
            worse, better = check_negative(name, cur["extra"], base_fp, fp_tolerance)
        else:
            worse, better = check_positive(name, cur["score"], base, tolerance)
        if worse:
            problems.append(worse)
        if better:
            improved.append(better)

    # Пропавший срез это тоже поломка: перестать мерить проще, чем починить.
    for name in sorted(base):
        if name not in rows:
            problems.append(f"{name}: срез пропал из замера, был {base[name]:.4f}")

    return problems, improved


def report(title, lines):
    """Печатает раздел отчёта, если в нём есть что показать."""
    if not lines:
        return
    print(title)
    for line in lines:
        print(f"  {line}")


def main():
    args = sys.argv[1:]

    if args and args[0] == "--init":
        return init_baseline(args[1])

    if len(args) != 2:
        print(__doc__, file=sys.stderr)
        return 2

    baseline_path, score_path = args
    with open(baseline_path, encoding="utf-8") as fh:
        baseline = json.load(fh)

    rows = parse(score_path)
    if not rows:
        print("вывод измерителя пуст или не разобран", file=sys.stderr)
        return 2

    problems, improved = compare(rows, baseline)
    report("Стало лучше:", improved)
    report("Стало хуже:", problems)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
