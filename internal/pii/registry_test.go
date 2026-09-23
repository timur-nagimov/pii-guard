package pii

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

// registryStubDetector отдаёт заранее заданный список фрагментов. Набор
// детекторов проверяется отдельно от настоящих детекторов, чтобы тест не падал
// при изменении их правил.
type registryStubDetector struct {
	types []Type
	spans []Span
}

func (s registryStubDetector) Types() []Type { return s.types }

func (s registryStubDetector) Detect(_ *Doc) []Span {
	out := make([]Span, len(s.spans))
	copy(out, s.spans)
	return out
}

// spanKeys превращает фрагменты в строки для наглядного сравнения в тестах.
func spanKeys(spans []Span) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, string(s.Type)+":"+itoa(s.Start)+"-"+itoa(s.End))
	}
	return out
}

// TestResolveOverlaps проверяет порядок правил снятия пересечений: сначала
// длина, затем приоритет типа, затем уверенность.
func TestResolveOverlaps(t *testing.T) {
	cases := []struct {
		name        string
		spans       []Span
		minConf     float64
		wantKept    []string
		wantDropped []string
	}{
		{
			name: "непересекающиеся фрагменты остаются все",
			spans: []Span{
				{Start: 0, End: 5, Type: TypePhone, Conf: ConfHigh},
				{Start: 10, End: 20, Type: TypeCard, Conf: ConfHigh},
			},
			minConf:  ConfMedium,
			wantKept: []string{"PHONE:0-5", "CARD:10-20"},
		},
		{
			name: "соприкасающиеся фрагменты не считаются пересекающимися",
			spans: []Span{
				{Start: 0, End: 5, Type: TypePhone, Conf: ConfHigh},
				{Start: 5, End: 10, Type: TypeCard, Conf: ConfHigh},
			},
			minConf:  ConfMedium,
			wantKept: []string{"PHONE:0-5", "CARD:5-10"},
		},
		{
			name: "из пересекающихся побеждает более длинный",
			spans: []Span{
				{Start: 0, End: 5, Type: TypeCard, Conf: ConfCertain},
				{Start: 3, End: 20, Type: TypeCVV, Conf: ConfLow + 0.2},
			},
			minConf:     ConfMedium,
			wantKept:    []string{"CVV:3-20"},
			wantDropped: []string{"CARD:0-5"},
		},
		{
			name: "при равной длине побеждает более приоритетный тип",
			spans: []Span{
				{Start: 0, End: 10, Type: TypeCVV, Conf: ConfCertain},
				{Start: 0, End: 10, Type: TypeCard, Conf: ConfMedium},
			},
			minConf:     ConfMedium,
			wantKept:    []string{"CARD:0-10"},
			wantDropped: []string{"CVV:0-10"},
		},
		{
			name: "при равном приоритете побеждает более уверенный",
			spans: []Span{
				{Start: 0, End: 10, Type: TypePhone, Conf: ConfMedium},
				{Start: 0, End: 10, Type: TypePhone, Conf: ConfCertain},
			},
			minConf:     ConfMedium,
			wantKept:    []string{"PHONE:0-10"},
			wantDropped: []string{"PHONE:0-10"},
		},
		{
			name: "фрагмент ниже порога уверенности отклоняется",
			spans: []Span{
				{Start: 0, End: 10, Type: TypeCard, Conf: ConfLow},
				{Start: 20, End: 30, Type: TypePhone, Conf: ConfHigh},
			},
			minConf:     ConfMedium,
			wantKept:    []string{"PHONE:20-30"},
			wantDropped: []string{"CARD:0-10"},
		},
		{
			name: "пустые и перевёрнутые фрагменты отбрасываются молча",
			spans: []Span{
				{Start: 5, End: 5, Type: TypeCard, Conf: ConfHigh},
				{Start: 10, End: 4, Type: TypeCard, Conf: ConfHigh},
				{Start: 20, End: 30, Type: TypePhone, Conf: ConfHigh},
			},
			minConf:  ConfMedium,
			wantKept: []string{"PHONE:20-30"},
		},
		{
			name: "длинный фрагмент вытесняет уже принятый короткий",
			spans: []Span{
				{Start: 0, End: 4, Type: TypeCard, Conf: ConfHigh},
				{Start: 2, End: 30, Type: TypeAddress, Conf: ConfMedium},
			},
			minConf:     ConfMedium,
			wantKept:    []string{"ADDRESS:2-30"},
			wantDropped: []string{"CARD:0-4"},
		},
		{
			name:     "пустой список",
			spans:    nil,
			minConf:  ConfMedium,
			wantKept: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, dropped := Resolve(append([]Span(nil), c.spans...), c.minConf)
			if got := spanKeys(kept); !reflect.DeepEqual(got, orEmpty(c.wantKept)) {
				t.Fatalf("принято %v, ожидалось %v", got, c.wantKept)
			}
			if got := spanKeys(dropped); !reflect.DeepEqual(got, orEmpty(c.wantDropped)) {
				t.Fatalf("отклонено %v, ожидалось %v", got, c.wantDropped)
			}
		})
	}
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// TestResolveReasons проверяет, что у отклонённых фрагментов остаётся причина:
// по ней собираются показатели и журнал разбора.
func TestResolveReasons(t *testing.T) {
	spans := []Span{
		{Start: 0, End: 10, Type: TypeCard, Conf: ConfLow, Reason: "card:shape"},
		{Start: 20, End: 24, Type: TypeCVV, Conf: ConfHigh, Reason: "cvv:anchor"},
		{Start: 20, End: 40, Type: TypeAddress, Conf: ConfHigh, Reason: "address:shape"},
	}
	_, dropped := Resolve(spans, ConfMedium)
	if len(dropped) != 2 {
		t.Fatalf("отклонено %d фрагментов, ожидалось 2", len(dropped))
	}
	byType := make(map[Type]string, len(dropped))
	for _, s := range dropped {
		byType[s.Type] = s.Reason
	}
	if got := byType[TypeCard]; !strings.Contains(got, "below_min_confidence") {
		t.Errorf("причина отклонения по уверенности = %q", got)
	}
	if got := byType[TypeCVV]; !strings.Contains(got, "overlap_lost") {
		t.Errorf("причина отклонения по пересечению = %q", got)
	}
	if got := byType[TypeCVV]; !strings.HasPrefix(got, "cvv:anchor") {
		t.Errorf("исходная причина затёрта: %q", got)
	}
}

// TestResolveKeptSorted проверяет, что принятые фрагменты отсортированы по
// началу: маскирование идёт одним проходом и требует именно такого порядка.
func TestResolveKeptSorted(t *testing.T) {
	spans := []Span{
		{Start: 50, End: 60, Type: TypePhone, Conf: ConfHigh},
		{Start: 0, End: 10, Type: TypeCard, Conf: ConfHigh},
		{Start: 25, End: 30, Type: TypeCVV, Conf: ConfHigh},
	}
	kept, _ := Resolve(spans, ConfMedium)
	for i := 1; i < len(kept); i++ {
		if kept[i-1].Start > kept[i].Start {
			t.Fatalf("порядок нарушен: %v", spanKeys(kept))
		}
	}
}

// TestSortSpansStable проверяет устойчивость сортировки: одинаковые по началу,
// длине и уверенности фрагменты сохраняют исходный порядок, иначе результат
// разбора становится невоспроизводимым.
func TestSortSpansStable(t *testing.T) {
	spans := []Span{
		{Start: 0, End: 10, Type: TypeFIO, Conf: ConfHigh, Reason: "первый"},
		{Start: 0, End: 10, Type: TypeAddress, Conf: ConfHigh, Reason: "второй"},
		{Start: 0, End: 10, Type: TypeEmail, Conf: ConfHigh, Reason: "третий"},
	}
	for i := 0; i < 20; i++ {
		cp := append([]Span(nil), spans...)
		SortSpans(cp)
		for j, s := range cp {
			if s.Reason != spans[j].Reason {
				t.Fatalf("порядок равных фрагментов изменился: %q на месте %d", s.Reason, j)
			}
		}
	}
}

// TestSortSpansOrder проверяет порядок сортировки: начало, затем убывание
// длины, затем убывание уверенности.
func TestSortSpansOrder(t *testing.T) {
	spans := []Span{
		{Start: 5, End: 8, Type: TypeCVV, Conf: ConfHigh},
		{Start: 0, End: 4, Type: TypeCard, Conf: ConfLow},
		{Start: 0, End: 4, Type: TypeCard, Conf: ConfHigh},
		{Start: 0, End: 9, Type: TypePhone, Conf: ConfLow},
	}
	SortSpans(spans)
	want := []string{"PHONE:0-9", "CARD:0-4", "CARD:0-4", "CVV:5-8"}
	if got := spanKeys(spans); !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок %v, ожидался %v", got, want)
	}
	if spans[1].Conf != ConfHigh {
		t.Fatalf("при равной длине первым должен идти более уверенный, получено %.2f", spans[1].Conf)
	}
}

// TestRegistryDetect проверяет, что набор собирает находки всех детекторов и
// отдаёт их отсортированными, не снимая пересечений.
func TestRegistryDetect(t *testing.T) {
	r := NewRegistry()
	r.Register(
		registryStubDetector{types: []Type{TypeCard}, spans: []Span{{Start: 10, End: 20, Type: TypeCard, Conf: ConfHigh}}},
		registryStubDetector{types: []Type{TypeCVV, TypeCard}, spans: []Span{{Start: 0, End: 5, Type: TypeCVV, Conf: ConfHigh}}},
	)
	got := spanKeys(r.Detect(NewDoc("неважно")))
	want := []string{"CVV:0-5", "CARD:10-20"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("находки %v, ожидались %v", got, want)
	}
	if n := len(r.Detectors()); n != 2 {
		t.Fatalf("зарегистрировано %d детекторов, ожидалось 2", n)
	}
	types := r.Types()
	if !reflect.DeepEqual(types, []Type{TypeCard, TypeCVV}) {
		t.Fatalf("типы набора %v, ожидались [CARD CVV]", types)
	}
}

// TestRegistryEmpty проверяет, что пустой набор не приводит к панике.
func TestRegistryEmpty(t *testing.T) {
	r := NewRegistry()
	if spans := r.Detect(NewDoc("текст 123")); len(spans) != 0 {
		t.Fatalf("пустой набор вернул %d фрагментов", len(spans))
	}
	if types := r.Types(); len(types) != 0 {
		t.Fatalf("пустой набор вернул типы %v", types)
	}
}

// TestPriority проверяет опорные значения приоритета: номер карты должен
// побеждать код безопасности, а неизвестный тип получает нулевой приоритет.
func TestPriority(t *testing.T) {
	if Priority(TypeCard) <= Priority(TypeCVV) {
		t.Error("номер карты должен быть приоритетнее кода безопасности")
	}
	if Priority(TypePassport) <= Priority(TypeFIO) {
		t.Error("паспорт должен быть приоритетнее имени")
	}
	if Priority(Type("НЕИЗВЕСТНЫЙ")) != 0 {
		t.Error("неизвестный тип должен получать нулевой приоритет")
	}
}

// TestSpanHelpers проверяет вспомогательные методы фрагмента.
func TestSpanHelpers(t *testing.T) {
	a := Span{Start: 0, End: 10}
	if a.Len() != 10 || !a.Valid() {
		t.Fatal("длина или признак корректности посчитаны неверно")
	}
	if (Span{Start: 5, End: 5}).Valid() || (Span{Start: 5, End: 1}).Valid() || (Span{Start: -1, End: 3}).Valid() {
		t.Fatal("пустой или перевёрнутый фрагмент признан корректным")
	}
	if !a.Overlaps(Span{Start: 9, End: 12}) {
		t.Error("пересечение не найдено")
	}
	if a.Overlaps(Span{Start: 10, End: 12}) {
		t.Error("соприкосновение принято за пересечение")
	}
}

// TestAllTypesUnique проверяет, что перечень обязательных типов не содержит
// повторов: по нему строится отчёт о качестве.
func TestAllTypesUnique(t *testing.T) {
	seen := make(map[Type]bool)
	for _, tp := range AllTypes() {
		if seen[tp] {
			t.Fatalf("тип %s встречается дважды", tp)
		}
		seen[tp] = true
	}
	if len(seen) != 23 {
		t.Fatalf("обязательных типов %d, ожидалось 23", len(seen))
	}
}

// Детектор типов из настроек обязан подменяться на ходу.
//
// До сменного слота он собирался один раз при запуске, и добавленный в
// настройки тип не действовал до перезапуска: журнал писал «настройки
// применены», сервис отвечал успехом, а нового типа в ответе не было.
func TestRegistrySetCustomTakesEffectImmediately(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewEmailDetector())

	doc := NewDoc("пропуск 123456 и почта a@b.ru")
	if got := len(reg.Detect(doc)); got != 1 {
		t.Fatalf("до подмены найдено фрагментов: %d, ожидался один (почта)", got)
	}

	badge, err := NewCustomDetector([]CustomRule{{
		Name: "BADGE", Pattern: `(\d{6})`, Group: 1,
		Anchors: []string{"пропуск"}, RequireAnchor: true,
	}})
	if err != nil {
		t.Fatalf("правило не собралось: %v", err)
	}
	reg.SetCustom(badge)

	spans := reg.Detect(doc)
	if len(spans) != 2 {
		t.Fatalf("после подмены найдено фрагментов: %d, ожидалось два\n%+v", len(spans), spans)
	}
	var seen bool
	for _, s := range spans {
		if s.Type == "BADGE" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("добавленный тип не найден: %+v", spans)
	}
	// Новый тип обязан быть виден и в перечне типов: по нему строится охват.
	var listed bool
	for _, tp := range reg.Types() {
		if tp == "BADGE" {
			listed = true
		}
	}
	if !listed {
		t.Errorf("добавленный тип не попал в перечень: %v", reg.Types())
	}

	// Снятие слота возвращает набор к исходному состоянию.
	reg.SetCustom(nil)
	if got := len(reg.Detect(doc)); got != 1 {
		t.Errorf("после снятия найдено фрагментов: %d, ожидался один", got)
	}
	for _, tp := range reg.Types() {
		if tp == "BADGE" {
			t.Error("снятый тип остался в перечне")
		}
	}
}

// Слот читают обработчики запросов, а меняет его наблюдение за файлом
// настроек. Проверка под -race.
func TestRegistrySetCustomIsRaceFree(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewEmailDetector())
	rule := []CustomRule{{Name: "BADGE", Pattern: `(\d{6})`, Group: 1}}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doc := NewDoc("пропуск 123456 и почта a@b.ru")
			for {
				select {
				case <-stop:
					return
				default:
					reg.Detect(doc)
					reg.Types()
				}
			}
		}()
	}

	for i := 0; i < 200; i++ {
		d, err := NewCustomDetector(rule)
		if err != nil {
			t.Errorf("правило не собралось: %v", err)
			break
		}
		reg.SetCustom(d)
		reg.SetCustom(nil)
	}
	close(stop)
	wg.Wait()
}
