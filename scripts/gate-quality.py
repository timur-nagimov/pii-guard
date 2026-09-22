#!/usr/bin/env python3
"""Сравнение качества с базовой линией.

Читает вывод cmd/score и файл базовой линии, печатает расхождения и возвращает
единицу, если стало хуже.

Правил два, и они разной строгости.

Положительные срезы сравниваются с допуском: замер шумит на сотые доли,
и придираться к четвёртому знаку значит закрывать ворота на ровном месте.
Допуск задаётся в самой базовой линии, по умолчанию 0.005.

Отрицательные срезы, имена которых начинаются на neg_, сравниваются строго
с нулём. В них нет персональных данных, поэтому любое изменение текста это
ложное срабатывание. Жюри проверяет это отдельным сценарием, и допуск здесь
означал бы разрешение портить пользовательский текст.

Использование:
    gate-quality.py baseline.json score-output.txt
    gate-quality.py --init score-output.txt > baseline.json
"""

import json
import re
import sys

# Строка вывода: имя среза, доля изменённого, доля затронутых, число фрагментов.
# Хвост про лишние байты необязателен и на сравнение не влияет.
ROW = re.compile(
    r"^(?P<name>[A-Za-zА-Яа-я_][\w./-]*)\s+"
    r"(?P<score>\d+\.\d+)\s+"
    r"(?P<touched>[\d.]+)%\s+"
    r"(?P<fragments>\d+)"
)

DEFAULT_TOLERANCE = 0.005


def parse(path):
    """Достаёт из вывода измерителя таблицу «срез → доля изменённого»."""
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
                "fragments": int(m.group("fragments")),
            }
    return rows


def main():
    args = sys.argv[1:]

    if args and args[0] == "--init":
        rows = parse(args[1])
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
            "срезы": {k: v["score"] for k, v in sorted(rows.items())},
        }
        print(json.dumps(out, ensure_ascii=False, indent=2))
        return 0

    if len(args) != 2:
        print(__doc__, file=sys.stderr)
        return 2

    baseline_path, score_path = args
    with open(baseline_path, encoding="utf-8") as fh:
        baseline = json.load(fh)
    base = baseline.get("срезы", {})
    tolerance = float(baseline.get("_допуск", DEFAULT_TOLERANCE))

    rows = parse(score_path)
    if not rows:
        print("вывод измерителя пуст или не разобран", file=sys.stderr)
        return 2

    problems = []
    improved = []

    for name, cur in sorted(rows.items()):
        score = cur["score"]

        if name.startswith("neg_"):
            # Строго ноль. Допуска нет намеренно.
            if score > 0:
                problems.append(
                    f"{name}: ложные срабатывания, изменено {score:.4f}, "
                    f"затронуто {cur['touched']:.1f}%, должно быть ровно 0"
                )
            continue

        if name not in base:
            # Новый срез это не повод закрывать ворота, но человек должен
            # знать, что базовая линия отстала.
            print(f"новый срез {name}: {score:.4f}, в базовой линии его нет",
                  file=sys.stderr)
            continue

        delta = score - base[name]
        if delta < -tolerance:
            problems.append(
                f"{name}: было {base[name]:.4f}, стало {score:.4f}, "
                f"просадка {-delta:.4f} при допуске {tolerance}"
            )
        elif delta > tolerance:
            improved.append(f"{name}: {base[name]:.4f} → {score:.4f} (+{delta:.4f})")

    for name in sorted(base):
        if name not in rows:
            problems.append(f"{name}: срез пропал из замера, был {base[name]:.4f}")

    if improved:
        print("Стало лучше:")
        for line in improved:
            print(f"  {line}")

    if problems:
        print("Стало хуже:")
        for line in problems:
            print(f"  {line}")
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
