package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"pii-guard/internal/pii"
)

// Правка файла настроек по месту.
//
// Почему не «разобрать в структуру и записать обратно». Файл настроек больше
// чем наполовину состоит из комментариев: там расчёты сроков, объяснения
// выбранных чисел, история того, почему значение именно такое. Обычная
// сериализация структуры их уничтожает, и человек, открывший файл после
// правки через интерфейс, теряет всё объяснение.
//
// Поэтому правим дерево узлов yaml.Node: оно сохраняет комментарии, порядок
// ключей и форматирование. Меняется ровно то, что попросили.

// EditResult — что получилось после правки.
type EditResult struct {
	// Applied — правка записана в файл.
	Applied bool
	// Message — что именно изменилось, человеческим языком.
	Message string
}

// SetSystemTypes задаёт список типов, которые маскирует система.
//
// Пустой список отвергается, потому что в этом формате он значит обратное
// ожидаемому: prepare трактует отсутствие списка как «все типы». Интерфейс,
// снявший последнюю галочку, получил бы полное маскирование вместо никакого —
// молча и незаметно. Чтобы система перестала обрабатывать запросы, её
// выключают признаком enabled.
func SetSystemTypes(path, system string, types []string) (EditResult, error) {
	if len(types) == 0 {
		return EditResult{}, fmt.Errorf("список типов пуст, а пустой список в этом формате означает «все типы»: чтобы выключить систему, есть признак enabled")
	}
	return editFile(path, func(root *yaml.Node) (string, error) {
		sys, err := systemNode(root, system)
		if err != nil {
			return "", err
		}
		if err := checkTypeNames(root, types); err != nil {
			return "", err
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, t := range types {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: t})
		}
		setMapValue(sys, "types", seq)
		return fmt.Sprintf("система %q маскирует типов: %d", system, len(types)), nil
	})
}

// checkTypeNames отвергает имена типов, которых не существует.
//
// Проверка настроек их пропускает: prepare просто кладёт любое имя в набор, и
// несуществующий тип никогда ни с чем не совпадёт. Через файл это видно
// глазами, через интерфейс — нет: человек снимает галочки, видит «сохранено»
// и получает систему, которая молча не маскирует то, что он выбрал. Лучше
// отказать с перечнем допустимых имён.
func checkTypeNames(root *yaml.Node, types []string) error {
	known := map[string]bool{"all": true}
	for _, t := range pii.AllTypes() {
		known[strings.ToLower(string(t))] = true
	}
	// Свои типы из этого же файла — такие же полноправные имена.
	if list := findMapValue(documentRoot(root), "custom_types"); list != nil {
		for _, item := range list.Content {
			if n := findMapValue(item, "name"); n != nil {
				known[strings.ToLower(n.Value)] = true
			}
		}
	}

	var unknown []string
	for _, t := range types {
		if !known[strings.ToLower(t)] {
			unknown = append(unknown, t)
		}
	}
	if len(unknown) == 0 {
		return nil
	}

	allowed := make([]string, 0, len(known))
	for k := range known {
		allowed = append(allowed, strings.ToUpper(k))
	}
	sort.Strings(allowed)
	return fmt.Errorf("неизвестные типы: %s; допустимые: %s",
		strings.Join(unknown, ", "), strings.Join(allowed, ", "))
}

// SetSystemEnabled включает или выключает систему целиком.
func SetSystemEnabled(path, system string, enabled bool) (EditResult, error) {
	return editFile(path, func(root *yaml.Node) (string, error) {
		sys, err := systemNode(root, system)
		if err != nil {
			return "", err
		}
		setMapValue(sys, "enabled", &yaml.Node{
			Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", enabled),
		})
		state := "выключена"
		if enabled {
			state = "включена"
		}
		return fmt.Sprintf("система %q %s", system, state), nil
	})
}

// AddCustomType добавляет свой тип персональных данных.
//
// Правило проверяется до записи: неверное выражение отвергается здесь, а не
// на следующем перечитывании настроек. Иначе сервис молча остался бы со
// старыми правилами, а человек считал бы, что добавил новый тип.
func AddCustomType(path string, ct CustomType) (EditResult, error) {
	if ct.Name == "" {
		return EditResult{}, fmt.Errorf("не задано имя типа")
	}
	if ct.Pattern == "" {
		return EditResult{}, fmt.Errorf("не задано выражение для поиска")
	}
	if _, err := pii.NewCustomDetector([]pii.CustomRule{{
		Name: ct.Name, Pattern: ct.Pattern, Group: ct.Group,
		Validator: ct.Validator, Anchors: ct.Anchors,
		RequireAnchor: ct.RequireAnchor, AnchorWindow: ct.AnchorWindow,
	}}); err != nil {
		return EditResult{}, fmt.Errorf("правило не принято: %w", err)
	}

	return editFile(path, func(root *yaml.Node) (string, error) {
		list := customTypesList(documentRoot(root))
		if customTypeIndex(list, string(ct.Name)) >= 0 {
			return "", fmt.Errorf("тип %q уже есть", ct.Name)
		}
		list.Content = append(list.Content, customTypeNode(&ct))
		return fmt.Sprintf("добавлен тип %q", ct.Name), nil
	})
}

// customTypesList возвращает список своих типов, заводя раздел, если его в
// файле ещё нет: добавление первого типа не должно требовать правки файла
// руками.
func customTypesList(doc *yaml.Node) *yaml.Node {
	if list := findMapValue(doc, "custom_types"); list != nil {
		return list
	}
	list := &yaml.Node{Kind: yaml.SequenceNode}
	setMapValue(doc, "custom_types", list)
	return list
}

// customTypeIndex ищет свой тип в списке по имени и возвращает его место или
// -1. Именно место, а не узел: по нему тип и добавляют, и убирают.
func customTypeIndex(list *yaml.Node, name string) int {
	for i, item := range list.Content {
		if n := findMapValue(item, "name"); n != nil && n.Value == name {
			return i
		}
	}
	return -1
}

// customTypeNode собирает узел описания своего типа.
//
// Необязательные поля не пишутся вовсе, когда равны нулю: файл настроек
// читают глазами, и строка вроде «group: 0» в нём выглядит настройкой, хотя
// означает её отсутствие. Признак require_anchor пишется всегда — от него
// зависит, ищется тип по всему тексту или только рядом с якорем, и
// умалчивать о таком нельзя.
func customTypeNode(ct *CustomType) *yaml.Node {
	item := &yaml.Node{Kind: yaml.MappingNode}
	setMapValue(item, "name", &yaml.Node{Kind: yaml.ScalarNode, Value: string(ct.Name)})
	setMapValue(item, "pattern", &yaml.Node{Kind: yaml.ScalarNode, Value: ct.Pattern, Style: yaml.SingleQuotedStyle})
	if ct.Group != 0 {
		setMapValue(item, "group", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", ct.Group)})
	}
	if ct.Validator != "" {
		setMapValue(item, "validator", &yaml.Node{Kind: yaml.ScalarNode, Value: ct.Validator})
	}
	if len(ct.Anchors) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, a := range ct.Anchors {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: a, Style: yaml.DoubleQuotedStyle})
		}
		setMapValue(item, "anchors", seq)
	}
	setMapValue(item, "require_anchor", &yaml.Node{
		Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", ct.RequireAnchor),
	})
	if ct.AnchorWindow != 0 {
		setMapValue(item, "anchor_window", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", ct.AnchorWindow)})
	}
	item.HeadComment = "добавлено через интерфейс"
	return item
}

// RemoveCustomType убирает свой тип персональных данных.
func RemoveCustomType(path, name string) (EditResult, error) {
	return editFile(path, func(root *yaml.Node) (string, error) {
		doc := documentRoot(root)
		list := findMapValue(doc, "custom_types")
		if list == nil {
			return "", fmt.Errorf("своих типов в настройках нет")
		}
		if i := customTypeIndex(list, name); i >= 0 {
			list.Content = append(list.Content[:i], list.Content[i+1:]...)
			return fmt.Sprintf("убран тип %q", name), nil
		}
		return "", fmt.Errorf("тип %q не найден среди своих: встроенные типы убрать нельзя, их отключают списком types у системы", name)
	})
}

// editFile применяет правку к дереву узлов и записывает файл, если новые
// настройки проходят проверку.
//
// Порядок важен: сначала правим копию в памяти, потом проверяем её целиком
// тем же кодом, что и при загрузке, и только потом пишем на диск. Записать
// сперва и проверить потом означало бы оставить сервис со сломанным файлом.
func editFile(path string, edit func(root *yaml.Node) (string, error)) (EditResult, error) {
	// Путь сюда приходит от оператора: его задаёт флаг -config при запуске, и
	// ручка правки правил берёт его из настроек сервера, а не из запроса.
	// Подставить свой путь снаружи нельзя. Приводим его к каноническому виду
	// один раз, чтобы проверка ниже сравнивала каталоги, а не их написания.
	path = filepath.Clean(path)

	raw, err := os.ReadFile(path) //nolint:gosec // путь задаёт оператор сервиса
	if err != nil {
		return EditResult{}, fmt.Errorf("файл настроек не прочитан: %w", err)
	}

	blanks := blankLinesPreservable(raw)
	src := raw
	if blanks {
		src = hideBlankLines(raw)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return EditResult{}, fmt.Errorf("файл настроек не разобран: %w", err)
	}

	msg, err := edit(&root)
	if err != nil {
		return EditResult{}, err
	}

	// Отступ в два пробела, как в исходном файле. Значение по умолчанию —
	// четыре, и без этой строки правка одного поля переписывает отступы во
	// всём файле: в истории изменений получается пятьсот строк вместо двух,
	// и понять, что именно поменяли через интерфейс, становится нельзя.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return EditResult{}, fmt.Errorf("настройки не собрались обратно: %w", err)
	}
	if err := enc.Close(); err != nil {
		return EditResult{}, fmt.Errorf("настройки не дописаны: %w", err)
	}
	out := buf.Bytes()
	if blanks {
		out = showBlankLines(out)
	}

	// Проверяем целиком тем же кодом, что и при загрузке: правка не должна
	// оставить файл в состоянии, которое сервис откажется принять.
	if _, err := Parse(out); err != nil {
		return EditResult{}, fmt.Errorf("после правки настройки не проходят проверку: %w", err)
	}

	// Пишем через временный файл в том же каталоге и переименование: так на
	// диске никогда не окажется половины файла, даже если процесс умрёт
	// посреди записи. Сервис перечитывает файл по таймеру и мог бы поймать
	// обрывок.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return EditResult{}, fmt.Errorf("временный файл не создан: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	// Временный файл обязан остаться в каталоге настроек: только тогда
	// переименование атомарно, потому что происходит внутри одной файловой
	// системы, и заведомо не задевает ничего за пределами каталога. Проверка
	// дешёвая, а цена незамеченного выхода — затёртый чужой файл.
	if filepath.Dir(tmpName) != dir {
		return EditResult{}, fmt.Errorf("временный файл %s оказался вне каталога настроек %s", tmpName, dir)
	}

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return EditResult{}, fmt.Errorf("временный файл не записан: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return EditResult{}, fmt.Errorf("временный файл не закрыт: %w", err)
	}
	// Права нового файла — как у прежнего: файл настроек нередко лежит с
	// урезанными правами, и правка через интерфейс не должна их расширять.
	if st, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmpName, st.Mode()) //nolint:gosec // путь задаёт оператор сервиса, каталог проверен выше
	}
	if err := os.Rename(tmpName, path); err != nil { //nolint:gosec // путь задаёт оператор сервиса, каталог проверен выше
		return EditResult{}, fmt.Errorf("файл настроек не заменён: %w", err)
	}

	return EditResult{Applied: true, Message: msg}, nil
}

// Сохранение пустых строк.
//
// Дерево узлов yaml хранит комментарии, но не пустые строки: для него это
// пробел между узлами, а не содержимое. В этом файле настроек пустые строки
// разделяют смысловые абзацы, и правка одного поля стирала бы их все разом.
//
// Приём: перед разбором каждая пустая строка превращается в комментарий-метку,
// который дерево сохраняет наравне с обычными комментариями, а после сборки
// метка превращается обратно в пустую строку.
const blankMarker = "#⟨пустая строка⟩"

// blankLinesPreservable сообщает, безопасен ли приём для этого файла.
//
// В многострочных значениях (| и >) пустая строка — часть самого значения, и
// подмена её комментарием исказила бы данные. Метка внутри файла тоже
// означала бы, что приём уже применяли к своему же выводу. В обоих случаях
// честнее потерять пустые строки, чем испортить настройки.
func blankLinesPreservable(raw []byte) bool {
	if bytes.Contains(raw, []byte(blankMarker)) {
		return false
	}
	return !blockScalar.Match(raw)
}

// blockScalar — строка вида «ключ: |» или «ключ: >-2», где указатель стоит
// последним. Знак | внутри значения (а он есть в каждом регулярном выражении
// этого файла) под это не подходит, поэтому проверка не срабатывает впустую.
var blockScalar = regexp.MustCompile(`(?m)^[^#\n]*:[ \t]*[|>][-+]?\d*[ \t]*$`)

// hideBlankLines заменяет пустые строки меткой-комментарием.
//
// Последняя строка файла после завершающего перевода строки пустая всегда и
// содержимым не является: метку туда ставить нечего.
func hideBlankLines(raw []byte) []byte {
	lines := bytes.Split(raw, []byte("\n"))
	for i, line := range lines[:max(0, len(lines)-1)] {
		if len(bytes.TrimSpace(line)) == 0 {
			lines[i] = []byte(blankMarker)
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// showBlankLines возвращает меткам вид пустых строк.
func showBlankLines(out []byte) []byte {
	lines := bytes.Split(out, []byte("\n"))
	for i, line := range lines {
		if string(bytes.TrimSpace(line)) == blankMarker {
			lines[i] = nil
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// documentRoot возвращает узел-отображение верхнего уровня.
func documentRoot(root *yaml.Node) *yaml.Node {
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		return root.Content[0]
	}
	return root
}

// systemNode находит узел описания системы по имени.
func systemNode(root *yaml.Node, system string) (*yaml.Node, error) {
	systems := findMapValue(documentRoot(root), "systems")
	if systems == nil {
		return nil, fmt.Errorf("в настройках нет раздела систем")
	}
	node := findMapValue(systems, system)
	if node == nil {
		return nil, fmt.Errorf("система %q не найдена, известные: %s", system, mapKeys(systems))
	}
	return node, nil
}

// findMapValue возвращает значение по ключу в узле-отображении.
func findMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMapValue заменяет значение по ключу либо добавляет пару в конец.
func setMapValue(m *yaml.Node, key string, value *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Комментарий над прежним значением сохраняем: он объясняет выбор.
			value.HeadComment = m.Content[i+1].HeadComment
			value.LineComment = m.Content[i+1].LineComment
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		value)
}

// mapKeys перечисляет ключи отображения через запятую.
func mapKeys(m *yaml.Node) string {
	if m == nil || m.Kind != yaml.MappingNode {
		return ""
	}
	var keys []string
	for i := 0; i+1 < len(m.Content); i += 2 {
		keys = append(keys, m.Content[i].Value)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}
