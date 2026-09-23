// Package dict содержит словари, которыми пользуется вид маскирования
// synthetic: правдоподобная подстановка берёт из них имена, фамилии и города.
// Выбор детерминирован по сиду, чтобы одно и то же значение в одном запросе
// всегда получало одну и ту же подстановку.
package dict

import (
	"sort"
	"strings"
)

// capitalize приводит слово к виду с заглавной первой буквой. Словари хранят
// имена строчными, а подстановка должна выглядеть как настоящее имя.
func capitalize(w string) string {
	if w == "" {
		return w
	}
	r := []rune(w)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// pickRandom выбирает элемент списка по детерминированному сиду. Список
// сортируется один раз при первом обращении, чтобы порядок не зависел от
// порядка в файле и от хеш-таблицы.
func pickRandom(list []string, seed uint64) string {
	if len(list) == 0 {
		return ""
	}
	return list[seed%uint64(len(list))]
}

// RandomMaleName возвращает случайное мужское имя с заглавной буквы.
func RandomMaleName(seed uint64) string {
	return capitalize(pickRandom(maleNames, seed))
}

// RandomFemaleName возвращает случайное женское имя с заглавной буквы.
func RandomFemaleName(seed uint64) string {
	return capitalize(pickRandom(femaleNames, seed))
}

// RandomSurname возвращает случайную фамилию с заглавной буквы.
func RandomSurname(seed uint64) string {
	return capitalize(pickRandom(surnameList, seed))
}

// RandomLatinName возвращает случайное латинское имя с заглавной буквы.
func RandomLatinName(seed uint64) string {
	return capitalize(pickRandom(latinNameList, seed))
}

// RandomLatinSurname возвращает случайную латинскую фамилию с заглавной буквы.
func RandomLatinSurname(seed uint64) string {
	return capitalize(pickRandom(latinSurnameList, seed))
}

// RandomCity возвращает случайный город из словаря с заглавной буквы.
func RandomCity(seed uint64) string {
	return capitalize(pickRandom(cityList, seed))
}

// RandomCountry возвращает случайную страну из словаря с заглавной буквы.
func RandomCountry(seed uint64) string {
	return capitalize(pickRandom(countryList, seed))
}

// maleNames, femaleNames, surnameList, latinNameList, latinSurnameList,
// cityList и countryList — отсортированные списки словарей для
// детерминированного выбора. Строятся один раз при первом обращении.
var (
	maleNames        = sortedKeys(data().male)
	femaleNames      = sortedKeys(data().female)
	surnameList      = sortedKeys(data().surnames)
	latinNameList    = sortedKeys(data().latinNames)
	latinSurnameList = sortedKeys(data().latinSurnames)
	cityList         = sortedCities()
	countryList      = sortedCountries()
)

// sortedKeys возвращает отсортированные ключи набора слов.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// sortedCities собирает отсортированный список городов из словаря.
func sortedCities() []string {
	out := make([]string, 0, len(cityNames))
	for _, c := range cityNames {
		out = append(out, NormalizePlace(c))
	}
	sort.Strings(out)
	return out
}

// sortedCountries собирает отсортированный список стран из словаря.
func sortedCountries() []string {
	out := make([]string, 0, len(countryNames))
	for _, c := range countryNames {
		out = append(out, NormalizePlace(c))
	}
	sort.Strings(out)
	return out
}
