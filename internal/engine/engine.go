// Package engine связывает обнаружение персональных данных, правила системы и
// маскирование в один конвейер обработки текста.
package engine

import (
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"pii-guard/internal/config"
	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// chunkTarget — целевой размер куска при разбиении длинного текста.
// Замер на стенде: один поток обрабатывает четыреста килобайт почти полсекунды,
// поэтому длинные тексты обязаны обрабатываться кусками параллельно.
const chunkTarget = 32 << 10

// chunkOverlap — перекрытие соседних кусков, чтобы фрагмент на границе
// нашёлся целиком хотя бы в одном из них.
const chunkOverlap = 512

// chunkThreshold — порог, начиная с которого текст режется на куски.
const chunkThreshold = 64 << 10

// Result — итог обработки одного текста.
type Result struct {
	// Text — текст с наложенными масками.
	Text string
	// Spans — принятые фрагменты в координатах исходного текста.
	Spans []pii.Span
	// Skipped — отклонённые фрагменты с причиной, для журнала и метрик.
	Skipped []pii.Span
	// Placeholders — подстановки для режима плейсхолдеров.
	Placeholders []mask.Placeholder
	// Counts — сколько фрагментов каждого типа найдено.
	Counts map[pii.Type]int
}

// Meta возвращает метаданные фрагментов без самих значений — их безопасно
// сохранять и писать в журнал.
func (r Result) Meta() []store.SpanMeta {
	out := make([]store.SpanMeta, 0, len(r.Spans))
	for _, s := range r.Spans {
		out = append(out, store.SpanMeta{Type: string(s.Type), Start: s.Start, End: s.End})
	}
	return out
}

// Engine — конвейер обработки. Между запросами хранит только готовые
// контекстные фильтры систем: их сборка разбирает словарь известных людей,
// и повторять её на каждом запросе незачем.
type Engine struct {
	registry *pii.Registry
	filters  sync.Map
	onPanic  PanicHandler

	// docPool переиспользует разобранные документы между запросами. Разбор
	// строит копию текста в нижнем регистре, срез токенов и индекс рун, и на
	// коротких текстах это самая дорогая по памяти часть запроса. Документ
	// живёт только внутри одного запроса и после ответа нигде не сохраняется,
	// поэтому пул безопасен по сроку жизни.
	docPool sync.Pool

	// chunkHook — точка вмешательства перед разбором куска. В рабочем режиме
	// она пустая и стоит ровно ноль: поле нужно тесту, которому иначе нечем
	// уронить панику именно внутри рабочей горутины, не подкладывая в рабочий
	// код детектор-диверсант.
	chunkHook func(chunk int)
}

// PanicHandler вызывается, когда обработка куска длинного текста завершилась
// сбоем. Форма повторяет pii.PanicHandler: движок сообщает о событии наружу и
// не зависит ни от пакета показателей, ни от журнала.
type PanicHandler func(chunk int, recovered any)

// New создаёт конвейер поверх набора детекторов.
func New(reg *pii.Registry) *Engine {
	e := &Engine{registry: reg}
	e.docPool.New = func() any { return &pii.Doc{} }
	return e
}

// getDoc берёт документ из пула и разбирает в него текст.
func (e *Engine) getDoc(text string) *pii.Doc {
	d := e.docPool.Get().(*pii.Doc)
	d.Parse(text)
	return d
}

// putDoc возвращает документ в пул после того, как запрос закончил с ним
// работу. Документ нигде не сохраняется, поэтому возврат безопасен.
func (e *Engine) putDoc(d *pii.Doc) {
	e.docPool.Put(d)
}

// OnPanic задаёт обработчик сбоя обработки куска. Задаётся один раз при сборке
// сервиса, до первого запроса, как и такой же обработчик у набора детекторов.
func (e *Engine) OnPanic(h PanicHandler) { e.onPanic = h }

// contextFilter возвращает готовый фильтр для системы, собирая его при первом
// обращении.
func (e *Engine) contextFilter(sys config.System) *pii.ContextFilter {
	if v, ok := e.filters.Load(sys.Name); ok {
		if f, valid := v.(*pii.ContextFilter); valid {
			return f
		}
	}
	f := pii.NewContextFilter(pii.ContextOptions{
		PublicFigures:  sys.Exclusions.PublicFigures,
		OrgAddresses:   sys.Exclusions.OrgAddresses,
		AllowPersons:   sys.Exclusions.AllowPersons,
		AllowAddresses: sys.Exclusions.AllowAddresses,
		AllowValues:    sys.Exclusions.AllowValues,
	})
	e.filters.Store(sys.Name, f)
	return f
}

// ResetFilters сбрасывает собранные фильтры. Вызывается после применения
// новых настроек, иначе система работала бы по прежним спискам исключений.
func (e *Engine) ResetFilters() { e.filters = sync.Map{} }

// filterDatesByMode убирает даты, найденные по контексту, если система
// требует только явный якорь.
func filterDatesByMode(spans []pii.Span, mode string) []pii.Span {
	if mode != config.DateAnchorOnly {
		return spans
	}
	out := spans[:0]
	for _, s := range spans {
		isDate := s.Type == pii.TypeDOB || s.Type == pii.TypeIssueDate
		if isDate && !strings.Contains(s.Reason, "_anchor") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Registry возвращает набор детекторов.
func (e *Engine) Registry() *pii.Registry { return e.registry }

// Mask находит персональные данные и накладывает маски по правилам системы.
func (e *Engine) Mask(text string, sys config.System, defs config.Defaults) Result {
	// Разбор текста делается один раз и переиспользуется всеми этапами:
	// и детекторами, и контекстными правилами. Разбор строит копию текста в
	// нижнем регистре, срез токенов и индекс рун, и на коротких текстах это
	// самая дорогая по памяти часть запроса. Документ берётся из пула, чтобы
	// срезы токенов и индекса рун переживали запрос и не выделялись заново.
	doc := e.getDoc(text)
	defer e.putDoc(doc)
	spans := e.detectDoc(doc)
	spans = filterByTypes(spans, sys)
	spans = filterDatesByMode(spans, sys.DateMode(defs))
	spans = applyContextRules(spans, sys, defs)

	// Контекстные правила снимают то, что формально похоже на персональные
	// данные, но ими не является: исторических лиц, адреса отделений, улицы,
	// названные в честь людей, и значения из списков разрешённых.
	spans, excluded := e.contextFilter(sys).Apply(doc, spans)

	kept, dropped := pii.Resolve(spans, sys.MinConf(defs))
	dropped = append(dropped, excluded...)
	applied := mask.Apply(text, kept, sys.MaskOptions(defs))

	counts := make(map[pii.Type]int, len(kept))
	for _, s := range kept {
		counts[s.Type]++
	}
	return Result{
		Text:         applied.Text,
		Spans:        kept,
		Skipped:      dropped,
		Placeholders: applied.Placeholders,
		Counts:       counts,
	}
}

// detect прогоняет детекторы по тексту, разбирая его самостоятельно.
func (e *Engine) detect(text string) []pii.Span {
	return e.detectDoc(pii.NewDoc(text))
}

// detectDoc прогоняет детекторы по уже разобранному документу, при
// необходимости кусками параллельно.
func (e *Engine) detectDoc(doc *pii.Doc) []pii.Span {
	text := doc.Text
	if len(text) <= chunkThreshold {
		return e.registry.Detect(doc)
	}

	bounds := splitBounds(text)
	results := make([][]pii.Span, len(bounds))
	workers := runtime.GOMAXPROCS(0)
	if workers > len(bounds) {
		workers = len(bounds)
	}

	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = e.detectChunk(text, bounds[i], i)
			}
		}()
	}
	for i := range bounds {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	return dedupSpans(results)
}

// detectChunk разбирает один кусок длинного текста, перехватывая сбой.
//
// Набор детекторов ловит панику самого детектора, но остальной код рабочей
// горутины не защищён ничем, а паника в горутине роняет процесс целиком:
// предохранитель обработчика живёт в другой горутине и дочернюю панику поймать
// не может. Обещание никогда не отвечать пятисотым кодом без этого перехвата
// не выполняется на текстах длиннее порога разбиения.
//
// Поведение при сбое такое же, как принято для сбоя детектора в pii.Registry:
// кусок пропускается, ответ отдаётся по остальным кускам, событие уходит
// наружу через обработчик. Перехват стоит на одном куске, а не на всей рабочей
// горутине, намеренно: горутина обязана вернуться к очереди заданий, иначе
// оставшиеся куски некому забрать и отправитель заданий встанет навсегда.
func (e *Engine) detectChunk(text string, bound [2]int, chunk int) (found []pii.Span) {
	defer func() {
		if rec := recover(); rec != nil {
			found = nil
			if e.onPanic != nil {
				e.onPanic(chunk, rec)
			}
		}
	}()
	if e.chunkHook != nil {
		e.chunkHook(chunk)
	}
	lo, hi := bound[0], bound[1]
	found = e.registry.Detect(pii.NewDoc(text[lo:hi]))
	for j := range found {
		found[j].Start += lo
		found[j].End += lo
	}
	return found
}

// splitBounds режет текст на куски по границам абзацев и предложений.
// Границы всегда попадают на начало руны, иначе кусок окажется битым.
func splitBounds(text string) [][2]int {
	var bounds [][2]int
	for start := 0; start < len(text); {
		end := start + chunkTarget
		if end >= len(text) {
			bounds = append(bounds, [2]int{start, len(text)})
			break
		}
		end = preferredBreak(text, start, end)
		bounds = append(bounds, [2]int{start, minInt(end+chunkOverlap, len(text))})
		start = end
	}
	return bounds
}

// preferredBreak ищет ближайшую слева границу абзаца или предложения.
func preferredBreak(text string, start, end int) int {
	lo := start + chunkTarget/2
	if lo < start {
		lo = start
	}
	for i := end; i > lo; i-- {
		if text[i-1] == '\n' {
			return alignRune(text, i)
		}
	}
	for i := end; i > lo; i-- {
		if text[i-1] == ' ' {
			return alignRune(text, i)
		}
	}
	return alignRune(text, end)
}

// alignRune сдвигает смещение назад до начала руны.
func alignRune(text string, off int) int {
	for off > 0 && off < len(text) && !utf8.RuneStart(text[off]) {
		off--
	}
	return off
}

// dedupSpans объединяет находки из кусков, убирая дубли из зоны перекрытия.
func dedupSpans(parts [][]pii.Span) []pii.Span {
	seen := make(map[[3]int]bool)
	var out []pii.Span
	for _, part := range parts {
		for _, s := range part {
			key := [3]int{s.Start, s.End, int(pii.Priority(s.Type))}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, s)
		}
	}
	pii.SortSpans(out)
	return out
}

// filterByTypes убирает фрагменты типов, которые система не маскирует.
func filterByTypes(spans []pii.Span, sys config.System) []pii.Span {
	out := spans[:0]
	for _, s := range spans {
		if sys.Allows(s.Type) {
			out = append(out, s)
		}
	}
	return out
}

// applyContextRules реализует правило «маскировать только в сочетании»:
// например, пин-код сам по себе не маскируется, а вместе с номером карты —
// маскируется. Правила включаются одним признаком в настройках.
func applyContextRules(spans []pii.Span, sys config.System, defs config.Defaults) []pii.Span {
	if !sys.ContextRulesOn(defs) || len(sys.ContextRules) == 0 {
		return spans
	}
	present := make(map[pii.Type]bool, len(spans))
	for _, s := range spans {
		present[s.Type] = true
	}
	blocked := make(map[pii.Type]bool)
	for _, rule := range sys.ContextRules {
		satisfied := false
		for _, req := range rule.Requires {
			if present[req] {
				satisfied = true
				break
			}
		}
		if !satisfied {
			blocked[rule.Mask] = true
		}
	}
	if len(blocked) == 0 {
		return spans
	}
	out := spans[:0]
	for _, s := range spans {
		if blocked[s.Type] {
			continue
		}
		out = append(out, s)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
