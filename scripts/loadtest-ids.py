"""Переписывает идентификаторы набора под один прогон нагрузки.

Сервис хранит соответствие идентификатора и текста, поэтому повторный прогон
с теми же идентификаторами попадает на записи предыдущего и меряет не свежую
работу сервиса, а остатки прошлой. Скрипт добавляет к каждому идентификатору
метку прогона, и записи перестают пересекаться.

Запуск: python3 scripts/loadtest-ids.py исходный-набор новый-набор метка
"""

import json
import sys


def main(argv):
    if len(argv) != 4:
        print("нужны три аргумента: исходный набор, новый набор, метка прогона", file=sys.stderr)
        return 2
    src, dst, tag = argv[1], argv[2], argv[3]
    with open(src, encoding="utf-8") as fin, open(dst, "w", encoding="utf-8") as fout:
        for line in fin:
            line = line.strip()
            if not line:
                continue
            item = json.loads(line)
            base = item.get("payload_id") or item.get("id") or "sample"
            item["payload_id"] = "{0}-{1}".format(base, tag)
            fout.write(json.dumps(item, ensure_ascii=False) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
