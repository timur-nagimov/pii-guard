package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Настройки для проверок правки: маленькие, но со всем, что можно сломать, —
// комментариями, пустыми строками между абзацами, своим типом и системой.
const editableConfig = `# Заголовок файла.
server:
  http: ":8080"
  # Срок записи ответа: объяснение, которое обязано пережить правку.
  write_timeout: 9s

defaults:
  types: [all]

systems:
  # Боевая система.
  kilo:
    enabled: true
    types: [all]
    key_hash: "0000000000000000000000000000000000000000000000000000000000000000"

custom_types:
  - name: SNILS
    pattern: '(\d{3}-\d{3}-\d{3}[ -]?\d{2})'
    group: 1
    validator: snils
`

func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(editableConfig), 0o600); err != nil {
		t.Fatalf("настройки не записаны: %v", err)
	}
	return path
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("настройки не прочитаны: %v", err)
	}
	return string(b)
}

// Главное свойство правки: меняется ровно то, что попросили, и ничего больше.
// Комментарии в этом файле объясняют выбор чисел, и правка через интерфейс не
// имеет права их стирать.
func TestEditKeepsEverythingExceptTheChange(t *testing.T) {
	path := writeConfig(t)

	if _, err := SetSystemTypes(path, "kilo", []string{"FIO", "PHONE"}); err != nil {
		t.Fatalf("типы не заданы: %v", err)
	}

	got := readConfig(t, path)

	for _, want := range []string{
		"# Заголовок файла.",
		"# Срок записи ответа: объяснение, которое обязано пережить правку.",
		"# Боевая система.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("комментарий пропал после правки: %q", want)
		}
	}
	if !strings.Contains(got, "\n\ndefaults:") {
		t.Error("пустая строка перед defaults пропала: абзацы файла слиплись")
	}
	if !strings.Contains(got, "\n  http: \":8080\"") {
		t.Error("отступ съехал: ожидалось два пробела")
	}
	if !strings.Contains(got, "types: [FIO, PHONE]") {
		t.Errorf("сама правка не применилась:\n%s", got)
	}

	// Различие должно быть ровно в одной строке.
	before := strings.Split(editableConfig, "\n")
	after := strings.Split(got, "\n")
	if len(before) != len(after) {
		t.Fatalf("число строк изменилось: было %d, стало %d\n%s", len(before), len(after), got)
	}
	changed := 0
	for i := range before {
		if before[i] != after[i] {
			changed++
			t.Logf("строка %d: %q → %q", i+1, before[i], after[i])
		}
	}
	if changed != 1 {
		t.Errorf("изменённых строк: %d, ожидалась одна", changed)
	}
}

// Добавление и удаление своего типа должны возвращать файл к исходному виду.
// Если не возвращают — правка тащит за собой невидимый мусор, и после
// нескольких правок через интерфейс файл перестаёт быть читаемым.
func TestAddRemoveCustomTypeIsIdentity(t *testing.T) {
	path := writeConfig(t)

	if _, err := AddCustomType(path, CustomType{
		Name: "BADGE", Pattern: `(\d{6})`, Group: 1,
		Anchors: []string{"пропуск"}, RequireAnchor: true,
	}); err != nil {
		t.Fatalf("тип не добавлен: %v", err)
	}

	added := readConfig(t, path)
	if !strings.Contains(added, "BADGE") {
		t.Fatalf("добавленного типа нет в файле:\n%s", added)
	}
	// Новый тип должен быть виден настройкам, а не просто лежать текстом.
	cfg, err := Parse([]byte(added))
	if err != nil {
		t.Fatalf("файл с добавленным типом не разбирается: %v", err)
	}
	found := false
	for _, ct := range cfg.CustomTypes {
		if ct.Name == "BADGE" {
			found = true
			if !ct.RequireAnchor || ct.Group != 1 || len(ct.Anchors) != 1 {
				t.Errorf("поля типа потерялись при записи: %+v", ct)
			}
		}
	}
	if !found {
		t.Error("добавленный тип не виден в разобранных настройках")
	}

	if _, err := RemoveCustomType(path, "BADGE"); err != nil {
		t.Fatalf("тип не убран: %v", err)
	}
	if got := readConfig(t, path); got != editableConfig {
		t.Errorf("после добавления и удаления файл отличается от исходного:\n--- было ---\n%s\n--- стало ---\n%s", editableConfig, got)
	}
}

// Неверная правка не имеет права коснуться файла. Иначе интерфейс оставил бы
// сервис с настройками, которые тот откажется перечитать, — и следующая
// перезагрузка стала бы отказом.
func TestRejectedEditsLeaveFileUntouched(t *testing.T) {
	cases := []struct {
		name string
		do   func(path string) error
		want string
	}{
		{
			name: "выражение не компилируется",
			do: func(p string) error {
				_, err := AddCustomType(p, CustomType{Name: "BROKEN", Pattern: `(\d{3}`})
				return err
			},
			want: "правило не принято",
		},
		{
			name: "имя типа пустое",
			do: func(p string) error {
				_, err := AddCustomType(p, CustomType{Pattern: `(\d{3})`})
				return err
			},
			want: "не задано имя типа",
		},
		{
			name: "выражение пустое",
			do: func(p string) error {
				_, err := AddCustomType(p, CustomType{Name: "EMPTY"})
				return err
			},
			want: "не задано выражение",
		},
		{
			name: "тип уже есть",
			do: func(p string) error {
				_, err := AddCustomType(p, CustomType{Name: "SNILS", Pattern: `(\d{3})`})
				return err
			},
			want: "уже есть",
		},
		{
			name: "встроенный тип не убрать",
			do: func(p string) error {
				_, err := RemoveCustomType(p, "FIO")
				return err
			},
			want: "не найден среди своих",
		},
		{
			name: "системы нет",
			do: func(p string) error {
				_, err := SetSystemTypes(p, "нет-такой", []string{"FIO"})
				return err
			},
			want: "не найдена",
		},
		{
			name: "пустой список типов",
			do: func(p string) error {
				_, err := SetSystemTypes(p, "kilo", nil)
				return err
			},
			want: "список типов пуст",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t)
			err := tc.do(path)
			if err == nil {
				t.Fatal("правка принята, хотя должна быть отвергнута")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("сообщение об ошибке не объясняет причину: %v", err)
			}
			if got := readConfig(t, path); got != editableConfig {
				t.Errorf("отвергнутая правка изменила файл:\n%s", got)
			}
		})
	}
}

// Проверка настроек целиком перед записью — последняя преграда между правкой
// и сломанным файлом. Через обычные операции её не достать: каждая отвергает
// плохой ввод раньше. Поэтому проверяется сам механизм: правка, после которой
// настройки перестают разбираться, не должна попасть на диск.
func TestEditFileRejectsConfigThatFailsValidation(t *testing.T) {
	path := writeConfig(t)

	_, err := editFile(path, func(root *yaml.Node) (string, error) {
		logging := &yaml.Node{Kind: yaml.MappingNode}
		setMapValue(logging, "level", &yaml.Node{Kind: yaml.ScalarNode, Value: "чепуха"})
		setMapValue(documentRoot(root), "logging", logging)
		return "правка, ломающая настройки", nil
	})
	if err == nil {
		t.Fatal("правка принята, хотя настройки после неё не проходят проверку")
	}
	if !strings.Contains(err.Error(), "не проходят проверку") {
		t.Errorf("отказ не объясняет причину: %v", err)
	}
	if got := readConfig(t, path); got != editableConfig {
		t.Errorf("файл изменён несмотря на отказ:\n%s", got)
	}
}

// Имя типа с опечаткой не должно доходить до файла: набор типов такое имя
// молча примет и никогда ни с чем не совпадёт.
func TestSetSystemTypesRejectsUnknownNames(t *testing.T) {
	path := writeConfig(t)

	_, err := SetSystemTypes(path, "kilo", []string{"FIO", "ФИО", "PNONE"})
	if err == nil {
		t.Fatal("несуществующие типы приняты")
	}
	for _, want := range []string{"ФИО", "PNONE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в отказе не названо имя %q: %v", want, err)
		}
	}
	// Перечень неизвестных идёт до точки с запятой, после неё — допустимые,
	// где FIO быть обязано. Смотреть надо только на первую часть.
	listed, _, _ := strings.Cut(err.Error(), ";")
	if strings.Contains(listed, "FIO") {
		t.Errorf("верное имя FIO названо неизвестным: %v", err)
	}
	if got := readConfig(t, path); got != editableConfig {
		t.Error("отвергнутая правка изменила файл")
	}

	// Свой тип из этого же файла — полноправное имя наравне со встроенными.
	if _, err := SetSystemTypes(path, "kilo", []string{"FIO", "SNILS", "all"}); err != nil {
		t.Errorf("свой тип SNILS не признан: %v", err)
	}
}

// Временные файлы после правки оставаться не должны: каталог настроек
// подхватывается перечитыванием, и обрывки там ни к чему.
func TestEditLeavesNoTempFiles(t *testing.T) {
	path := writeConfig(t)
	dir := filepath.Dir(path)

	if _, err := SetSystemEnabled(path, "kilo", false); err != nil {
		t.Fatalf("система не выключена: %v", err)
	}
	if _, err := AddCustomType(path, CustomType{Name: "X", Pattern: `(\d)`}); err != nil {
		t.Fatalf("тип не добавлен: %v", err)
	}
	_, _ = AddCustomType(path, CustomType{Name: "Y", Pattern: `(\d{3}`}) // заведомо отвергается

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("каталог не прочитан: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "config.yaml" {
			t.Errorf("лишний файл после правки: %s", e.Name())
		}
	}
}

// Выключение системы должно доходить до разобранных настроек, а не только до
// текста файла.
func TestSetSystemEnabled(t *testing.T) {
	path := writeConfig(t)

	if _, err := SetSystemEnabled(path, "kilo", false); err != nil {
		t.Fatalf("система не выключена: %v", err)
	}
	cfg, err := Parse([]byte(readConfig(t, path)))
	if err != nil {
		t.Fatalf("настройки не разбираются: %v", err)
	}
	if cfg.Systems["kilo"].Enabled {
		t.Error("система осталась включённой")
	}

	if _, err := SetSystemEnabled(path, "kilo", true); err != nil {
		t.Fatalf("система не включена обратно: %v", err)
	}
	cfg, err = Parse([]byte(readConfig(t, path)))
	if err != nil {
		t.Fatalf("настройки не разбираются: %v", err)
	}
	if !cfg.Systems["kilo"].Enabled {
		t.Error("система не включилась обратно")
	}
}

// Если в файле есть многострочное значение, пустая строка внутри него — часть
// данных, и подменять её меткой нельзя. Проверка должна это распознавать.
func TestBlankLinePreservationBacksOffOnBlockScalars(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"обычный файл", editableConfig, true},
		{"многострочное значение", "a: |\n  строка\n\n  ещё\n", false},
		{"многострочное с отсечкой", "a: |-\n  строка\n", false},
		{"свёрнутое значение", "a: >2\n   строка\n", false},
		{"знак | внутри выражения", "pattern: '(\\d|\\w)'\n", true},
		{"знак > в значении", "note: \"a > b\"\n", true},
		{"метка уже внутри", "a: 1 " + blankMarker + "\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := blankLinesPreservable([]byte(tc.body)); got != tc.want {
				t.Errorf("blankLinesPreservable = %v, ожидалось %v", got, tc.want)
			}
		})
	}
}
