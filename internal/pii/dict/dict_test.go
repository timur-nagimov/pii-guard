package dict

import (
	"strings"
	"testing"
)

// TestDictionariesLoad проверяет, что встроенные словари загрузились и не
// пусты: без них детектор имён не найдёт ни одного имени.
func TestDictionariesLoad(t *testing.T) {
	male, female, surname := Sizes()
	if male == 0 || female == 0 || surname == 0 {
		t.Fatalf("словари пусты: мужские %d, женские %d, фамилии %d", male, female, surname)
	}
	names, surnames := LatinSizes()
	if names == 0 || surnames == 0 {
		t.Fatalf("латинские словари пусты: имена %d, фамилии %d", names, surnames)
	}
}

// TestFiguresReturnsCopy проверяет, что Figures возвращает копию: изменение
// возвращённого списка не трогает общий словарь.
func TestFiguresReturnsCopy(t *testing.T) {
	first := Figures()
	if len(first) == 0 {
		t.Fatal("список известных людей пуст")
	}
	first[0] = "изменено"
	second := Figures()
	if second[0] == "изменено" {
		t.Fatal("изменение копии затронуло общий словарь")
	}
}

// TestFiguresLowercaseNoDupes проверяет, что имена и фамилии в списке известных
// людей в нижнем регистре и без повторов.
func TestFiguresLowercaseNoDupes(t *testing.T) {
	seen := make(map[string]bool, len(figures))
	for _, f := range figures {
		if f != strings.ToLower(f) {
			t.Fatalf("запись не в нижнем регистре: %q", f)
		}
		if seen[f] {
			t.Fatalf("повтор записи: %q", f)
		}
		seen[f] = true
	}
}

// TestPlaceFindsCity проверяет, что словарь находит город и страну.
func TestPlaceFindsCity(t *testing.T) {
	if kind, ok := Place("Москва"); !ok || kind != KindCity {
		t.Fatalf("Москва не найдена как город: %q, %v", kind, ok)
	}
	if kind, ok := Place("москва"); !ok || kind != KindCity {
		t.Fatalf("москва строчными не найдена: %q, %v", kind, ok)
	}
	if kind, ok := Place("Россия"); !ok || kind != KindCountry {
		t.Fatalf("Россия не найдена как страна: %q, %v", kind, ok)
	}
	if !IsCity("Санкт-Петербург") {
		t.Fatal("Санкт-Петербург не опознан как город")
	}
	if !IsCountry("РФ") {
		t.Fatal("РФ не опознана как страна")
	}
}

// TestPlaceRejectsGarbage проверяет, что мусор не находится в словаре.
func TestPlaceRejectsGarbage(t *testing.T) {
	for _, name := range []string{"", "   ", "несуществующийгород", "12345", "Москваа"} {
		if _, ok := Place(name); ok {
			t.Fatalf("мусор %q найден в словаре", name)
		}
	}
}

// TestNormalizePlace проверяет приведение названия к словарному виду.
func TestNormalizePlace(t *testing.T) {
	if got := NormalizePlace("  Москва  "); got != "москва" {
		t.Fatalf("нормализация: %q", got)
	}
	if got := NormalizePlace("Ёлкино"); got != "елкино" {
		t.Fatalf("буква ё не заменена: %q", got)
	}
	if got := NormalizePlace("Нижний   Новгород"); got != "нижний новгород" {
		t.Fatalf("пробелы не схлопнуты: %q", got)
	}
}

// TestInflectPlace проверяет порождение косвенных форм названий.
func TestInflectPlace(t *testing.T) {
	// «Тула» даёт формы предложного и родительного падежей.
	forms := inflectPlace("тула")
	if len(forms) == 0 {
		t.Fatal("для «тула» формы не порождены")
	}
	// Составные названия не склоняются.
	if got := inflectPlace("нижний новгород"); got != nil {
		t.Fatalf("составное название склонено: %v", got)
	}
	// Короткие названия не склоняются.
	if got := inflectPlace("уфа"); got != nil {
		t.Fatalf("короткое название склонено: %v", got)
	}
	// Название на гласную, кроме а/я/ь, не склоняется.
	if got := inflectPlace("иваново"); got != nil {
		t.Fatalf("название на гласную склонено: %v", got)
	}
}

// TestLookupName проверяет поиск имени в начальной и падежной форме.
func TestLookupName(t *testing.T) {
	if g, ok := LookupName("Иван"); !ok || g != GenderMale {
		t.Fatalf("Иван не найден как мужское имя: %v, %v", g, ok)
	}
	if g, ok := LookupName("Мария"); !ok || g != GenderFemale {
		t.Fatalf("Мария не найдена как женское имя: %v, %v", g, ok)
	}
	if _, ok := LookupName("несуществующееимя"); ok {
		t.Fatal("несуществующее имя найдено")
	}
}

// TestLookupSurname проверяет поиск фамилии.
func TestLookupSurname(t *testing.T) {
	if !LookupSurname("Иванов") {
		t.Fatal("Иванов не найден как фамилия")
	}
	if LookupSurname("несуществующаяфамилия") {
		t.Fatal("несуществующая фамилия найдена")
	}
}

// TestKnownWord проверяет, что слово из любого словаря опознаётся.
func TestKnownWord(t *testing.T) {
	if !KnownWord("Иван") {
		t.Fatal("Иван не опознан как известное слово")
	}
	if !KnownWord("Иванов") {
		t.Fatal("Иванов не опознан как известное слово")
	}
	if KnownWord("неизвестноеслово") {
		t.Fatal("неизвестное слово опознано")
	}
}

// TestTranslitToCyr проверяет обратную транслитерацию латиницы.
func TestTranslitToCyr(t *testing.T) {
	if got := TranslitToCyr("ivanov"); got != "иванов" {
		t.Fatalf("ivanov -> %q", got)
	}
	if got := TranslitToCyr("olga"); got != "олга" {
		t.Fatalf("olga -> %q", got)
	}
	if got := TranslitToCyr("shch"); got != "щ" {
		t.Fatalf("shch -> %q", got)
	}
}

// TestTranslitToLatin проверяет прямую транслитерацию кириллицы.
func TestTranslitToLatin(t *testing.T) {
	if got := TranslitToLatin("ольга"); got != "olga" {
		t.Fatalf("ольга -> %q", got)
	}
	if got := TranslitToLatin("татьяна"); got != "tatyana" {
		t.Fatalf("татьяна -> %q", got)
	}
}

// TestLookupLatin проверяет поиск латинских имён и фамилий.
func TestLookupLatin(t *testing.T) {
	if !LookupLatinName("John") {
		t.Fatal("John не найден как латинское имя")
	}
	if !LookupLatinSurname("Smith") {
		t.Fatal("Smith не найден как латинская фамилия")
	}
	if LookupLatinName("неизвестное") {
		t.Fatal("неизвестное латинское имя найдено")
	}
}

// TestTranslitY проверяет чтение одиночной латинской y по соседям: после
// гласной это й, в конце слова после согласной это ий, иначе ы.
func TestTranslitY(t *testing.T) {
	if got := translitY("ма", "may", 2); got != "й" {
		t.Fatalf("после гласной: %q", got)
	}
	if got := translitY("иван", "ivany", 4); got != "ий" {
		t.Fatalf("в конце после согласной: %q", got)
	}
	if got := translitY("б", "byl", 1); got != "ы" {
		t.Fatalf("в середине: %q", got)
	}
}

// TestLastRune проверяет последнюю руну строки.
func TestLastRune(t *testing.T) {
	if got := lastRune(""); got != 0 {
		t.Fatalf("пустая строка: %q", got)
	}
	if got := lastRune("абв"); got != 'в' {
		t.Fatalf("последняя руна: %q", got)
	}
}
