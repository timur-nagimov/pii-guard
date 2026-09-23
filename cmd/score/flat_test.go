// Плоские входы замера держим в тестовом файле, а не рядом с самим замером:
// зовут их только проверки покрытия со второй ветки, а сам замер ходит через
// scanner, sliceStats и setStats. В рабочих исходниках это мёртвый код, и
// длинный список параметров там читается как долг, которого у замера нет.

package main

import (
	"pii-guard/internal/config"
	"pii-guard/internal/engine"
)

// scoreSample — плоский вход того же замера: срезы приходят тремя отдельными
// картами, конвейер — тремя отдельными значениями, а счётчик напечатанных
// примеров передаётся и возвращается числом. В таком виде замер одного
// элемента пришёл со второй ветки вместе с её проверками покрытия. Внутри он
// собирает scanner, sliceStats и examplePrinter и зовёт scoreSliceSample:
// второй реализации замера нет, и разойтись двум входам не на чем.
// Возвращает число уже напечатанных примеров. Счётчик shown у этого входа
// копился между элементами в цикле; сейчас его везде передают нулём, но
// выбросить параметр значит сломать вход и его проверки покрытия, поэтому
// unparam здесь заглушен осознанно.
//
//nolint:unparam // shown — часть формы входа со второй ветки, убирать нельзя
func scoreSample(s sample, eng *engine.Engine, sys config.System, defs config.Defaults,
	lower bool, examples int, onlyType string, shown int,
	byType, byCategory, bySource map[string]*stat) int {
	acc := &sliceStats{byType: byType, byCategory: byCategory, bySource: bySource}
	ex := &examplePrinter{limit: examples, onlyType: onlyType, shown: shown}
	scoreSliceSample(&scanner{eng: eng, sys: sys, defs: defs}, &s, lower, acc, ex)
	return ex.shown
}

// scoreDatasetSample — плоский вход замера по наборам: карты тип×набор и по
// набору приходят отдельно, конвейер — тремя значениями. Пара к scoreSample,
// пришёл оттуда же и так же зовёт scoreSetSample.
func scoreDatasetSample(s sample, eng *engine.Engine, sys config.System, defs config.Defaults,
	lower bool, setName string, byType map[string]map[string]*stat, bySet map[string]*stat) {
	acc := &setStats{byType: byType, bySet: bySet}
	scoreSetSample(&scanner{eng: eng, sys: sys, defs: defs}, &s, lower, acc, setName)
}
