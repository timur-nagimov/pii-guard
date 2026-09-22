// Команда corpus собирает набор для проверки качества из нескольких
// источников.
//
// Подкоманда inject берёт настоящие тексты из открытых источников и вставляет
// в них синтетические значения персональных данных с точными границами: так
// получаются положительные примеры с живым контекстом, а не с придуманным.
//
// Подкоманда merge сводит файлы разных источников в один набор, проверяет
// каждый элемент и печатает сводку.
//
// Запуск:
//
//	go run ./cmd/corpus inject -in corpus/wiki.jsonl -out corpus/wiki-inj.jsonl -seed 7
//	go run ./cmd/corpus merge -out corpus/all.jsonl -seed 42 corpus/dataset.jsonl corpus/wiki-inj.jsonl
package main

import (
	"fmt"
	"os"
)

// usage печатает подсказку по подкомандам.
func usage() {
	fmt.Fprint(os.Stderr, `corpus: сборка набора для проверки качества

Подкоманды:
  inject   вставить синтетические значения в настоящие тексты
  merge    свести файлы источников в один проверенный набор

Подсказка по флагам подкоманды: go run ./cmd/corpus <подкоманда> -h
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "inject":
		err = runInject(os.Args[2:])
	case "merge":
		err = runMerge(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}
}
