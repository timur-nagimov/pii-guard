package engine

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"pii-guard/internal/config"
	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
)

// newTestEngine собирает конвейер на форматном детекторе и детекторе адресов
// почты. Этих двух детекторов достаточно, чтобы проверить сам конвейер, и при
// этом тест не зависит от детекторов, которые пишутся параллельно.
func newTestEngine() *Engine {
	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector())
	return New(reg)
}

// parseSystem разбирает настройки одной системы из текста. Разбор через
// настоящий загрузчик заполняет служебные поля, которые руками легко забыть.
func parseSystem(t *testing.T, name, yaml string) (config.System, config.Defaults) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	sys, ok := cfg.System(name)
	if !ok {
		t.Fatalf("система %q не найдена в настройках", name)
	}
	return sys, cfg.Defaults
}

// allTypesSystem возвращает систему, которая маскирует всё звёздочками.
func allTypesSystem(t *testing.T) (config.System, config.Defaults) {
	t.Helper()
	return parseSystem(t, "anon", `
systems:
  anon:
    enabled: true
    auth: none
    types: [all]
    preset: full
    min_confidence: 0.5
`)
}

// hasSpan сообщает, есть ли среди фрагментов ровно такой по границам и типу.
func hasSpan(spans []pii.Span, start, end int, tp pii.Type) bool {
	for _, s := range spans {
		if s.Start == start && s.End == end && s.Type == tp {
			return true
		}
	}
	return false
}

// TestMaskSimple проверяет, что конвейер находит и маскирует персональные
// данные в коротком тексте.
func TestMaskSimple(t *testing.T) {
	sys, defs := allTypesSystem(t)
	res := newTestEngine().Mask("Телефон +79161234567, почта ivan@example.com", sys, defs)

	if strings.Contains(res.Text, "79161234567") || strings.Contains(res.Text, "ivan@example.com") {
		t.Fatalf("персональные данные остались в тексте: %q", res.Text)
	}
	if !strings.HasPrefix(res.Text, "Телефон ") {
		t.Fatalf("окружающий текст изменился: %q", res.Text)
	}
	if res.Counts[pii.TypePhone] != 1 || res.Counts[pii.TypeEmail] != 1 {
		t.Fatalf("счётчики типов %+v", res.Counts)
	}
	meta := res.Meta()
	if len(meta) != len(res.Spans) {
		t.Fatalf("метаданных %d, фрагментов %d", len(meta), len(res.Spans))
	}
	for i, m := range meta {
		if m.Type != string(res.Spans[i].Type) || m.Start != res.Spans[i].Start || m.End != res.Spans[i].End {
			t.Fatalf("метаданные %d не совпали с фрагментом: %+v", i, m)
		}
	}
}

// TestMaskEmptyText проверяет пустой текст.
func TestMaskEmptyText(t *testing.T) {
	sys, defs := allTypesSystem(t)
	res := newTestEngine().Mask("", sys, defs)
	if res.Text != "" || len(res.Spans) != 0 {
		t.Fatalf("получено %q и %d фрагментов", res.Text, len(res.Spans))
	}
}

// TestChunkedDetectionFindsSpanOnBoundary проверяет главное требование к
// разбиению длинного текста: фрагмент, стоящий ровно на границе кусков, обязан
// найтись целиком за счёт перекрытия соседних кусков.
func TestChunkedDetectionFindsSpanOnBoundary(t *testing.T) {
	const phone = "+79161234567"
	// Заполнитель без пробелов и переводов строки: тогда граница куска
	// приходится ровно на chunkTarget и её положение известно точно.
	filler := strings.Repeat("x", chunkTarget)
	// Номер ставится так, чтобы он пересекал границу первого куска.
	prefix := filler[:chunkTarget-6]
	text := prefix + phone + strings.Repeat("y", chunkThreshold)

	if len(text) <= chunkThreshold {
		t.Fatalf("текст длиной %d байт не попадает под разбиение", len(text))
	}
	start := len(prefix)
	end := start + len(phone)
	if start >= chunkTarget || end <= chunkTarget {
		t.Fatalf("номер %d:%d не пересекает границу куска %d", start, end, chunkTarget)
	}

	eng := newTestEngine()
	spans := eng.detect(text)
	if !hasSpan(spans, start, end, pii.TypePhone) {
		t.Fatalf("номер на границе кусков не найден, найдено %d фрагментов", len(spans))
	}

	sys, defs := allTypesSystem(t)
	res := eng.Mask(text, sys, defs)
	if strings.Contains(res.Text, phone) {
		t.Fatal("номер на границе кусков остался незамаскированным")
	}
	if res.Counts[pii.TypePhone] != 1 {
		t.Fatalf("номер найден %d раз, ожидался один раз", res.Counts[pii.TypePhone])
	}
}

// TestChunkedDetectionMatchesWholeText проверяет, что разбиение на куски не
// меняет результата: длинный текст обрабатывается так же, как короткий.
func TestChunkedDetectionMatchesWholeText(t *testing.T) {
	piece := "Телефон +79161234567 и почта ivan@example.com, далее. "
	long := strings.Repeat(piece, chunkThreshold/len(piece)+10)
	if len(long) <= chunkThreshold {
		t.Fatalf("текст длиной %d байт не попадает под разбиение", len(long))
	}
	sys, defs := allTypesSystem(t)
	res := newTestEngine().Mask(long, sys, defs)

	wantPhones := strings.Count(long, "+79161234567")
	if res.Counts[pii.TypePhone] != wantPhones {
		t.Fatalf("найдено %d номеров, ожидалось %d", res.Counts[pii.TypePhone], wantPhones)
	}
	if strings.Contains(res.Text, "+79161234567") {
		t.Fatal("в длинном тексте остался незамаскированный номер")
	}
	if len([]rune(res.Text)) != len([]rune(long)) {
		t.Fatalf("длина в рунах изменилась: %d вместо %d", len([]rune(res.Text)), len([]rune(long)))
	}
}

// TestSplitBoundsCoverText проверяет, что куски покрывают текст целиком,
// перекрываются и не режут руны.
func TestSplitBoundsCoverText(t *testing.T) {
	text := strings.Repeat("Иванов Иван, телефон +79161234567. ", 4000)
	bounds := splitBounds(text)
	if len(bounds) < 2 {
		t.Fatalf("текст длиной %d байт разбит на %d кусков", len(text), len(bounds))
	}
	if bounds[0][0] != 0 {
		t.Fatalf("первый кусок начинается с %d", bounds[0][0])
	}
	if last := bounds[len(bounds)-1]; last[1] != len(text) {
		t.Fatalf("последний кусок заканчивается на %d, текст длиной %d", last[1], len(text))
	}
	for i, b := range bounds {
		if b[0] < 0 || b[1] > len(text) || b[0] >= b[1] {
			t.Fatalf("кусок %d имеет границы %d:%d", i, b[0], b[1])
		}
		if i > 0 && bounds[i-1][1] < b[0] {
			t.Fatalf("между кусками %d и %d есть пропуск текста", i-1, i)
		}
		for _, r := range text[b[0]:b[1]] {
			if r == '�' {
				t.Fatalf("кусок %d разрезал руну", i)
			}
		}
	}
}

// TestFilterByTypes проверяет, что система маскирует только разрешённые ей
// типы, а остальное остаётся в тексте нетронутым.
func TestFilterByTypes(t *testing.T) {
	sys, defs := parseSystem(t, "only_phone", `
systems:
  only_phone:
    enabled: true
    auth: none
    types: [PHONE]
    preset: full
    min_confidence: 0.5
`)
	text := "Телефон +79161234567, почта ivan@example.com, паспорт 4509 123456"
	res := newTestEngine().Mask(text, sys, defs)

	if strings.Contains(res.Text, "+79161234567") {
		t.Fatal("разрешённый тип не замаскирован")
	}
	if !strings.Contains(res.Text, "ivan@example.com") {
		t.Fatal("адрес почты замаскирован, хотя тип системе не разрешён")
	}
	if !strings.Contains(res.Text, "4509 123456") {
		t.Fatal("паспорт замаскирован, хотя тип системе не разрешён")
	}
	if len(res.Counts) != 1 || res.Counts[pii.TypePhone] != 1 {
		t.Fatalf("счётчики типов %+v", res.Counts)
	}
}

// TestFilterByTypesAll проверяет, что перечень «all» разрешает все типы.
func TestFilterByTypesAll(t *testing.T) {
	sys, defs := allTypesSystem(t)
	text := "Телефон +79161234567, почта ivan@example.com"
	res := newTestEngine().Mask(text, sys, defs)
	if len(res.Counts) != 2 {
		t.Fatalf("счётчики типов %+v, ожидались два типа", res.Counts)
	}
}

// contextRulesYAML описывает систему с правилом «пин-код маскируется только
// вместе с номером карты».
const contextRulesYAML = `
systems:
  ctx:
    enabled: true
    auth: none
    types: [all]
    preset: full
    min_confidence: 0.5
    context_rules_enabled: true
    context_rules:
      - mask: PIN
        requires: [CARD]
`

// TestContextRuleDropsWithoutCompanion проверяет, что без требуемого соседа
// тип снимается с маскирования.
func TestContextRuleDropsWithoutCompanion(t *testing.T) {
	sys, defs := parseSystem(t, "ctx", contextRulesYAML)
	res := newTestEngine().Mask("Мой пин 1234, запомни", sys, defs)

	if res.Counts[pii.TypePIN] != 0 {
		t.Fatalf("пин-код замаскирован без номера карты: %q", res.Text)
	}
	if !strings.Contains(res.Text, "1234") {
		t.Fatalf("текст изменился, хотя правило сняло тип: %q", res.Text)
	}
}

// TestContextRuleKeepsWithCompanion проверяет, что при наличии требуемого
// соседа тип маскируется.
func TestContextRuleKeepsWithCompanion(t *testing.T) {
	sys, defs := parseSystem(t, "ctx", contextRulesYAML)
	res := newTestEngine().Mask("Карта 4111 1111 1111 1111, пин 1234", sys, defs)

	if res.Counts[pii.TypeCard] != 1 {
		t.Fatalf("номер карты не найден: %q", res.Text)
	}
	if res.Counts[pii.TypePIN] != 1 {
		t.Fatalf("пин-код не замаскирован рядом с номером карты: %q", res.Text)
	}
	if strings.Contains(res.Text, "1234,") || strings.Contains(res.Text, "4111") {
		t.Fatalf("значения остались в тексте: %q", res.Text)
	}
}

// TestContextRuleDisabled проверяет, что при выключенных контекстных правилах
// одиночный пин-код маскируется: иначе он уйдёт в языковую модель открытым.
func TestContextRuleDisabled(t *testing.T) {
	sys, defs := parseSystem(t, "ctx", `
systems:
  ctx:
    enabled: true
    auth: none
    types: [all]
    preset: full
    min_confidence: 0.5
    context_rules_enabled: false
    context_rules:
      - mask: PIN
        requires: [CARD]
`)
	res := newTestEngine().Mask("Мой пин 1234, запомни", sys, defs)
	if res.Counts[pii.TypePIN] != 1 {
		t.Fatalf("одиночный пин-код не замаскирован: %q", res.Text)
	}
}

// TestMinConfidence проверяет, что порог уверенности системы отсекает слабые
// находки и они попадают в отклонённые с причиной.
func TestMinConfidence(t *testing.T) {
	high, defs := parseSystem(t, "strict", `
systems:
  strict:
    enabled: true
    auth: none
    types: [all]
    preset: full
    min_confidence: 0.95
`)
	text := "паспорт серии 4509 номер 123456 и снилс 112-233-445 95"
	res := newTestEngine().Mask(text, high, defs)
	for _, s := range res.Spans {
		if s.Conf < 0.95 {
			t.Fatalf("принят фрагмент с уверенностью %.2f ниже порога", s.Conf)
		}
	}
	if len(res.Skipped) == 0 {
		t.Fatal("ни один фрагмент не попал в отклонённые, хотя порог высокий")
	}
	for _, s := range res.Skipped {
		if s.Reason == "" {
			t.Fatal("у отклонённого фрагмента не заполнена причина")
		}
	}
}

// TestMaskPresetFromSystem проверяет, что конвейер берёт пресет из настроек
// системы, включая переопределение по типу.
func TestMaskPresetFromSystem(t *testing.T) {
	sys, defs := parseSystem(t, "tokens", `
systems:
  tokens:
    enabled: true
    auth:
      header: X-Key
      key_sha256: "0000000000000000000000000000000000000000000000000000000000000000"
    types: [all]
    preset: token
    min_confidence: 0.5
`)
	res := newTestEngine().Mask("Телефон +79161234567 и он же +79161234567", sys, defs)
	if strings.Count(res.Text, "[PHONE_1]") != 2 {
		t.Fatalf("повтор значения получил разные плейсхолдеры: %q", res.Text)
	}
	if len(res.Placeholders) != 1 {
		t.Fatalf("сохранено %d подстановок, ожидалась одна", len(res.Placeholders))
	}
	if res.Placeholders[0].Value != "+79161234567" || res.Placeholders[0].Type != pii.TypePhone {
		t.Fatalf("подстановка собрана неверно: %+v", res.Placeholders[0])
	}
	if !mask.PresetToken.Valid() {
		t.Fatal("пресет плейсхолдеров должен быть известен")
	}
}

// TestEngineConcurrent проверяет, что один конвейер обслуживает запросы
// параллельно и отдаёт одинаковый результат.
func TestEngineConcurrent(t *testing.T) {
	sys, defs := allTypesSystem(t)
	eng := newTestEngine()
	text := "Телефон +79161234567, почта ivan@example.com, паспорт 4509 123456"
	want := eng.Mask(text, sys, defs).Text

	done := make(chan string, 16)
	for i := 0; i < 16; i++ {
		go func() { done <- eng.Mask(text, sys, defs).Text }()
	}
	for i := 0; i < 16; i++ {
		if got := <-done; got != want {
			t.Fatalf("параллельный вызов дал %q вместо %q", got, want)
		}
	}
}

// TestRegistryAccessible проверяет, что конвейер отдаёт свой набор детекторов:
// по нему сервис публикует перечень поддерживаемых типов.
func TestRegistryAccessible(t *testing.T) {
	eng := newTestEngine()
	if eng.Registry() == nil {
		t.Fatal("набор детекторов недоступен")
	}
	if len(eng.Registry().Types()) == 0 {
		t.Fatal("набор детекторов не объявил ни одного типа")
	}
}

// --- Сбой обработки куска длинного текста ---
//
// Длинный текст режется на куски, и каждый кусок разбирает рабочая горутина.
// Паника в горутине роняет процесс целиком: предохранитель обработчика живёт в
// другой горутине и дочернюю панику поймать не может. Набор детекторов ловит
// только панику самого детектора, весь остальной код горутины до перехвата не
// был защищён ничем. Тесты ниже держат этот перехват на месте.

// chunkPanicPhone — значение, по которому видно, какие куски были разобраны.
const chunkPanicPhone = "+79161234567"

// longChunkedText собирает текст длиннее порога разбиения, в котором телефон
// встречается в каждом куске.
func longChunkedText() string {
	piece := "Телефон " + chunkPanicPhone + " и почта ivan@example.com, далее. "
	// Кусков должно быть больше, чем рабочих горутин: иначе каждая горутина
	// заберёт ровно одно задание и зависание отправителя заданий не проявится.
	return strings.Repeat(piece, chunkTarget*(runtime.GOMAXPROCS(0)+2)/len(piece))
}

// countFrom считает вхождения значения, начинающиеся не раньше смещения off.
func countFrom(text, value string, off int) int {
	n := 0
	for i := off; i < len(text); {
		j := strings.Index(text[i:], value)
		if j < 0 {
			break
		}
		n++
		i += j + len(value)
	}
	return n
}

// TestChunkPanicDoesNotKillProcess — главный тест по сбою куска. Паника
// роняется внутри рабочей горутины, а не внутри детектора, то есть ровно там,
// где её раньше не ловил никто. Проверяется, что процесс жив, ответ отдан по
// остальным кускам, а событие ушло наружу через обработчик.
func TestChunkPanicDoesNotKillProcess(t *testing.T) {
	sys, defs := allTypesSystem(t)
	eng := newTestEngine()

	var mu sync.Mutex
	var reported []int
	var recovered any
	eng.OnPanic(func(chunk int, rec any) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, chunk)
		recovered = rec
	})
	eng.chunkHook = func(chunk int) {
		if chunk == 0 {
			panic("сбой разбора куска")
		}
	}

	text := longChunkedText()
	bounds := splitBounds(text)
	if len(bounds) < 3 {
		t.Fatalf("текст длиной %d байт разбит на %d кусков, нужно хотя бы три", len(text), len(bounds))
	}

	// Если перехвата нет, процесс умирает здесь и до проверок дело не доходит.
	res := eng.Mask(text, sys, defs)

	mu.Lock()
	gotReported := append([]int(nil), reported...)
	gotRecovered := recovered
	mu.Unlock()

	if len(gotReported) != 1 || gotReported[0] != 0 {
		t.Fatalf("о сбое сообщено по кускам %v, ожидался ровно один кусок с номером 0", gotReported)
	}
	if gotRecovered != "сбой разбора куска" {
		t.Fatalf("обработчику передано %v вместо значения паники", gotRecovered)
	}
	if len([]rune(res.Text)) != len([]rune(text)) {
		t.Fatalf("длина ответа в рунах %d вместо %d", len([]rune(res.Text)), len([]rune(text)))
	}

	// Соседние куски сохранили свои смещения: пропуск куска не сдвигает
	// найденное в остальных. Ожидаемое число — все телефоны, начинающиеся не
	// раньше начала второго куска.
	want := countFrom(text, chunkPanicPhone, bounds[1][0])
	if want == 0 {
		t.Fatal("во втором и следующих кусках нет ни одного телефона, тест ничего не проверяет")
	}
	if res.Counts[pii.TypePhone] != want {
		t.Fatalf("замаскировано %d телефонов, ожидалось %d", res.Counts[pii.TypePhone], want)
	}
	for _, s := range res.Spans {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			t.Fatalf("фрагмент с границами %d:%d не лежит в исходном тексте длиной %d", s.Start, s.End, len(text))
		}
		if got := text[s.Start:s.End]; s.Type == pii.TypePhone && got != chunkPanicPhone {
			t.Fatalf("смещения фрагмента сползли: по границам %d:%d стоит %q", s.Start, s.End, got)
		}
	}

	// Пропущенный кусок остался неразобранным — это заявленная деградация,
	// а не ошибка теста.
	if !strings.Contains(res.Text, chunkPanicPhone) {
		t.Fatal("пропущенный кусок оказался разобран, значит паника не сработала")
	}
}

// TestChunkPanicEveryChunkStillAnswers проверяет крайний случай: падают все
// куски. Ответ всё равно отдаётся, а рабочие горутины возвращаются к очереди
// заданий — иначе отправитель заданий встал бы навсегда и тест не завершился.
func TestChunkPanicEveryChunkStillAnswers(t *testing.T) {
	sys, defs := allTypesSystem(t)
	eng := newTestEngine()

	var mu sync.Mutex
	failures := 0
	eng.OnPanic(func(int, any) {
		mu.Lock()
		failures++
		mu.Unlock()
	})
	eng.chunkHook = func(int) { panic("сбой разбора куска") }

	text := longChunkedText()
	bounds := splitBounds(text)
	res := eng.Mask(text, sys, defs)

	mu.Lock()
	got := failures
	mu.Unlock()

	if got != len(bounds) {
		t.Fatalf("о сбое сообщено %d раз при %d кусках", got, len(bounds))
	}
	if res.Text != text {
		t.Fatal("текст изменился, хотя все куски пропущены")
	}
	if len(res.Spans) != 0 {
		t.Fatalf("при пропуске всех кусков принято %d фрагментов", len(res.Spans))
	}
}

// TestChunkPanicHandlerSilentOnHealthyText проверяет обратную сторону:
// на исправном тексте обработчик сбоя не вызывается ни разу, то есть показатель
// сбоев не шумит на рабочей нагрузке.
func TestChunkPanicHandlerSilentOnHealthyText(t *testing.T) {
	sys, defs := allTypesSystem(t)
	eng := newTestEngine()

	var mu sync.Mutex
	failures := 0
	eng.OnPanic(func(int, any) {
		mu.Lock()
		failures++
		mu.Unlock()
	})

	text := longChunkedText()
	res := eng.Mask(text, sys, defs)

	mu.Lock()
	got := failures
	mu.Unlock()

	if got != 0 {
		t.Fatalf("на исправном тексте сообщено о %d сбоях", got)
	}
	if want := strings.Count(text, chunkPanicPhone); res.Counts[pii.TypePhone] != want {
		t.Fatalf("замаскировано %d телефонов, ожидалось %d", res.Counts[pii.TypePhone], want)
	}
}

// TestSplitBoundsAlignedToRunes закрепляет свойство, нарушение которого нашло
// ночное ревью кода: обе границы куска обязаны попадать на начало руны.
//
// Левая граница выравнивалась, правая считалась как end+chunkOverlap и на
// границу руны не попадала. Кусок обрывался посреди многобайтовой руны, и
// токенизатор отдавал для неё символ замены. Кириллическая буква занимает два
// байта, поэтому на русском тексте это срабатывает regularly, а не в
// экзотическом случае.
func TestSplitBoundsAlignedToRunes(t *testing.T) {
	// Текст заведомо длиннее порога разбиения, целиком из двухбайтовых рун,
	// чтобы граница по байтам почти никогда не совпадала с границей руны.
	var sb strings.Builder
	for sb.Len() < chunkThreshold*3 {
		sb.WriteString("Клиент Иванов Иван Иванович, тел +7 916 123-45-67. ")
	}
	text := sb.String()

	bounds := splitBounds(text)
	if len(bounds) < 2 {
		t.Fatalf("текст на %d байт должен был разбиться, кусков получилось %d", len(text), len(bounds))
	}

	for i, b := range bounds {
		lo, hi := b[0], b[1]
		if lo < 0 || hi > len(text) || lo > hi {
			t.Fatalf("кусок %d: границы %d..%d вне текста длиной %d", i, lo, hi, len(text))
		}
		if lo < len(text) && !utf8.RuneStart(text[lo]) {
			t.Errorf("кусок %d: левая граница %d стоит посреди руны", i, lo)
		}
		if hi < len(text) && !utf8.RuneStart(text[hi]) {
			t.Errorf("кусок %d: правая граница %d стоит посреди руны", i, hi)
		}
		// Срез по границам обязан быть корректным UTF-8: именно это ломалось.
		if !utf8.ValidString(text[lo:hi]) {
			t.Errorf("кусок %d: срез text[%d:%d] не является корректным UTF-8", i, lo, hi)
		}
	}
}
