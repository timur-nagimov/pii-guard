#!/usr/bin/env python3
"""Сравнение скорости горячего пути с базовой линией.

Зачем. Ночной прогон дал регрессию на треть, и ни одна из шестнадцати проверок
её не увидела: ворота смотрели на качество и на правильность, но не на скорость.
Регрессия дошла бы до сдачи. Эта проверка закрывает пробел.

Как. Прогоняется короткий бенчмарк маскирования и сравнивается с записанными
числами. Допуск намеренно широкий: бенчмарк на занятой машине шумит, и закрыть
ворота из-за шума хуже, чем пропустить пятипроцентную просадку. Ловить надо
крупное, вроде той самой трети.

Базовую линию, как и для качества, меняет только человек: линейка, которую
двигает тот же, кто правит код, ничего не измеряет.

Использование:
    gate_perf.py baseline.json bench-output.txt
    gate_perf.py --init bench-output.txt > baseline.json
"""

import json
import re
import sys

# Строка вывода go test: имя, число прогонов, наносекунды на операцию.
ROW = re.compile(r"^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+(\d+(?:\.\d+)?)\s+ns/op")

DEFAULT_TOLERANCE = 0.25


def parse(path):
    """Достаёт из вывода go test замеры: имя бенчмарка и наносекунды.

    Строки, не похожие на замер, молча пропускаются: рядом с замерами идут
    шапка, служебные сообщения и итог, и падать на них значит требовать
    чистоты от чужого формата.
    """
    rows = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            m = ROW.match(line.strip())
            if m:
                rows[m.group(1)] = float(m.group(2))
    return rows


def init_baseline(path):
    """Снимает базовую линию с вывода бенчмарка."""
    rows = parse(path)
    if not rows:
        print("не разобрано ни одной строки бенчмарка", file=sys.stderr)
        return 2
    print(json.dumps({
        "_комментарий": (
            "Базовая линия скорости горячего пути, наносекунды на операцию. "
            "Меняет только человек и только осознанно. Числа зависят от "
            "машины, поэтому снимать надо там же, где потом сравнивать."
        ),
        "_допуск": DEFAULT_TOLERANCE,
        "замеры": dict(sorted(rows.items())),
    }, ensure_ascii=False, indent=2))
    return 0


def compare(want, got, tol):
    """Сводит замер с базовой линией: что замедлилось и что ускорилось.

    Пропавший замер не закрывает ворота: бенчмарк могли переименовать, и
    ронять прогон на этом значит наказывать за правку имени, а не за скорость.
    """
    problems, better = [], []
    for name, ref in sorted(want.items()):
        if name not in got:
            print(f"замер {name} пропал из прогона", file=sys.stderr)
            continue
        cur = got[name]
        delta = (cur - ref) / ref
        if delta > tol:
            problems.append(
                f"{name}: было {ref:.0f} нс, стало {cur:.0f} нс, "
                f"медленнее на {delta*100:.0f}% при допуске {tol*100:.0f}%"
            )
        elif delta < -tol:
            better.append(f"{name}: {ref:.0f} → {cur:.0f} нс ({-delta*100:.0f}% быстрее)")
    return problems, better


def report(title, lines):
    """Печатает раздел отчёта, если в нём есть что показать."""
    if not lines:
        return
    print(title)
    for line in lines:
        print(f"  {line}")


def compare_with_baseline(baseline_path, bench_path):
    """Сводит прогон бенчмарка с базовой линией и даёт код возврата.

    Вынесено из main отдельно от разбора аргументов: сравнение читается
    одним куском, а у main остаётся по одному выходу на каждый режим.
    """
    with open(baseline_path, encoding="utf-8") as fh:
        base = json.load(fh)
    want = base.get("замеры", {})
    tol = float(base.get("_допуск", DEFAULT_TOLERANCE))

    got = parse(bench_path)
    if not got:
        print("бенчмарк не отработал или вывод не разобран", file=sys.stderr)
        return 2

    problems, better = compare(want, got, tol)
    report("Стало быстрее:", better)
    report("Стало медленнее:", problems)
    return 1 if problems else 0


def main():
    """Выбирает режим по аргументам: снять базовую линию или сравнить с ней.

    Ворота смотрят на код возврата, поэтому ошибка вызова отделена от
    провала замера: двойка означает «померить не вышло», единица — «стало
    медленнее».
    """
    args = sys.argv[1:]

    if args and args[0] == "--init":
        return init_baseline(args[1])

    if len(args) != 2:
        print(__doc__, file=sys.stderr)
        return 2

    return compare_with_baseline(args[0], args[1])


if __name__ == "__main__":
    sys.exit(main())
