package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/logging"
	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
)

// inspectRequest — тело запроса на разбор текста.
type inspectRequest struct {
	// Text — текст для разбора.
	Text string `json:"text"`
	// Preset переопределяет вид маскирования только для этого запроса.
	// Пустое значение означает вид, заданный системе в настройках.
	Preset string `json:"preset,omitempty"`
	// System выбирает профиль системы-потребителя по имени. Пустое значение
	// означает систему, опознанную по ключу доступа. Поле нужно странице
	// проверки, которая переключает профили, не зная ключей.
	System string `json:"system,omitempty"`
}

// inspectSpan — один найденный фрагмент со всеми подробностями решения.
type inspectSpan struct {
	Type string `json:"type"`
	// Start и End — границы в байтах исходного текста.
	Start int `json:"start"`
	End   int `json:"end"`
	// StartRune и EndRune — те же границы в рунах: их удобнее сопоставлять
	// с тем, что видит человек, потому что кириллическая буква занимает два байта.
	StartRune int `json:"start_rune"`
	EndRune   int `json:"end_rune"`
	// Value — сам найденный фрагмент. Возвращается только тому, кто прислал
	// текст, то есть данные из ответа он и так уже знает. В журнал значение
	// не попадает никогда.
	Value string `json:"value"`
	// Masked — во что превратился фрагмент.
	Masked string `json:"masked"`
	// Confidence — уверенность решения от нуля до единицы.
	Confidence float64 `json:"confidence"`
	// Reason — по каким признакам принято решение: якорь, форма, словарь,
	// контрольная сумма. Это главное поле для отладки правил.
	Reason string `json:"reason"`
}

// inspectSkipped — фрагмент, снятый с маскирования, и причина.
type inspectSkipped struct {
	Type       string  `json:"type"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// inspectEdge — основание считать два фрагмента относящимися к одному человеку.
// Индексы ссылаются на элементы массива spans ответа.
type inspectEdge struct {
	// A и B — индексы фрагментов в массиве spans.
	A int `json:"a"`
	B int `json:"b"`
	// Basis — основание связи: sentence, anchor или repeat.
	Basis string `json:"basis"`
	// Confidence — уверенность связи от нуля до единицы.
	Confidence float64 `json:"confidence"`
}

// inspectSubject — один человек: набор фрагментов, отнесённых к нему.
type inspectSubject struct {
	// Fragments — индексы фрагментов в массиве spans.
	Fragments []int `json:"fragments"`
	// Types — типы персональных данных, найденные у субъекта.
	Types []string `json:"types"`
}

// inspectResponse — итог разбора.
type inspectResponse struct {
	// System — от имени какой системы выполнен разбор.
	System string `json:"system"`
	// Preset — применённый вид маскирования.
	Preset string `json:"preset"`
	// Masked — текст с наложенными масками.
	Masked string `json:"masked"`
	// Restored — обратное преобразование маски. Заполняется только для видов
	// маскирования, которые обратимы по месту; для остальных сервис опирается
	// на сохранённое соответствие, и здесь остаётся пусто.
	Restored string `json:"restored,omitempty"`
	// Spans — найденные фрагменты.
	Spans []inspectSpan `json:"spans"`
	// Skipped — фрагменты, снятые контекстными правилами.
	Skipped []inspectSkipped `json:"skipped"`
	// Edges — основания считать пары фрагментов относящимися к одному человеку.
	// По ним видно, почему фрагменты объединены в субъекта.
	Edges []inspectEdge `json:"edges"`
	// Subjects — разбиение фрагментов на субъектов: сколько людей в тексте и
	// какие фрагменты к кому относятся.
	Subjects []inspectSubject `json:"subjects"`
	// Counts — сколько фрагментов каждого типа.
	Counts map[string]int `json:"counts"`
	// Placeholders — подстановки для вида с плейсхолдерами.
	Placeholders map[string]string `json:"placeholders,omitempty"`
	// TookMs — длительность разбора в миллисекундах.
	TookMs float64 `json:"took_ms"`
	// TextBytes и TextRunes — размер текста.
	TextBytes int `json:"text_bytes"`
	TextRunes int `json:"text_runes"`
}

// handleInspect разбирает текст и показывает, что именно найдено, где и
// почему. Ручка нужна для проверки и отладки правил: обычный контракт
// возвращает только результат, а по нему не видно, какое правило сработало.
//
// Значения фрагментов в ответе есть, и это не утечка: их прислал сам
// вызывающий. В журнал и в показатели они по-прежнему не попадают.
func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только POST")
		return
	}

	cfg := s.Config()

	var req inspectRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, cfg.Server.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		s.writeValidation(w, r, "json_invalid", []string{fieldBody}, "не удалось разобрать JSON")
		return
	}
	if req.Text == "" {
		s.writeValidation(w, r, "missing", []string{fieldBody, "text"}, "поле text обязательно и не может быть пустым")
		return
	}

	// Страница проверки выбирает профиль системы по имени; ключ доступа ей не
	// нужен. Имя задано — подменяем систему, имени нет — остаёмся на системе,
	// опознанной по ключу.
	sys, ok := s.resolveRequestSystem(r, cfg, req.System)
	if !ok {
		s.writeError(w, r, http.StatusForbidden, "system_not_allowed", "система не опознана или отключена")
		return
	}
	if !sys.InspectOn(cfg.Defaults) {
		s.writeError(w, r, http.StatusForbidden, "inspect_disabled", "разбор текста для этой системы выключен настройкой")
		return
	}

	// Разовое переопределение вида маскирования: удобно сравнивать виды на
	// одном и том же тексте, не трогая настройки.
	if p := mask.Preset(req.Preset); req.Preset != "" {
		if !p.Valid() {
			s.writeValidation(w, r, "value_error", []string{fieldBody, "preset"}, "неизвестный вид маскирования")
			return
		}
		sys.Preset = p
		sys.PerType = nil
	}

	started := time.Now()
	res := s.engine.Mask(req.Text, sys, cfg.Defaults)
	took := time.Since(started)

	// Сборка ответа вынесена из обработчика отдельной функцией: обработчик
	// занимается разбором запроса и проверками, а перевод результата движка в
	// форму ответа живёт рядом с этой формой и проверяется сам по себе.
	out := buildInspectResponse(req.Text, res, sys, cfg.Defaults, took)

	s.writeJSON(w, http.StatusOK, out)
	// Идентификатор запроса и имя системы в записи не повторяются: их
	// подставляет обработчик журнала из контекста запроса.
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "разбор текста",
		logging.Event(logging.EventInspect),
		logging.Component("api"),
		slog.Int(logging.FieldBytes, out.TextBytes),
		slog.Int("spans", len(out.Spans)),
		slog.Int("skipped", len(out.Skipped)),
		logging.Took(took),
		slog.Any("pii_types", out.Counts))
	// Ручка разбора отдаёт значения персональных данных вызывающему, пусть и
	// его собственные. Для службы контроля это такое же обращение к данным,
	// как маскирование, поэтому оно идёт в аудит наравне с ним.
	s.auditProcess(r, sys, processEvent{
		dir: "inspect", size: out.TextBytes, counts: out.Counts, took: took,
	}, "ok")
}

// buildInspectResponse собирает ответ разбора из результата движка: фрагменты,
// снятые с маскирования, связи, субъектов и подстановки. Каждая часть переводится
// своей функцией — так видно, что именно собирается, и каждую можно проверить
// отдельно.
func buildInspectResponse(text string, res engine.Result, sys config.System, def config.Defaults, took time.Duration) inspectResponse {
	return inspectResponse{
		System:       sys.Name,
		Preset:       string(sys.MaskOptions(def).Default),
		Masked:       res.Text,
		Counts:       countsToStrings(res),
		TookMs:       float64(took.Microseconds()) / 1000,
		TextBytes:    len(text),
		TextRunes:    len([]rune(text)),
		Spans:        inspectSpans(text, res.Text, res.Spans),
		Skipped:      inspectSkippedSpans(text, res.Skipped),
		Edges:        inspectEdges(res.Edges),
		Subjects:     inspectSubjects(res.Subjects),
		Placeholders: inspectPlaceholders(res.Placeholders),
	}
}

// inspectSubjects переводит субъектов движка в форму ответа разбора.
func inspectSubjects(subjects []engine.Subject) []inspectSubject {
	out := make([]inspectSubject, 0, len(subjects))
	for _, sub := range subjects {
		types := make([]string, 0, len(sub.Types))
		for _, t := range sub.Types {
			types = append(types, string(t))
		}
		out = append(out, inspectSubject{
			Fragments: sub.Fragments,
			Types:     types,
		})
	}
	return out
}

// inspectSpans переводит найденные фрагменты в форму ответа разбора: к каждому
// добавляются рунные границы и кусок маски, вставший на его место.
func inspectSpans(text, masked string, spans []pii.Span) []inspectSpan {
	out := make([]inspectSpan, 0, len(spans))
	runeAt := runeOffsets(text)
	maskedRunes := []rune(masked)
	origRunes := []rune(text)
	for _, sp := range spans {
		item := inspectSpan{
			Type:       string(sp.Type),
			Start:      sp.Start,
			End:        sp.End,
			StartRune:  runeAt[sp.Start],
			EndRune:    runeAt[sp.End],
			Value:      text[sp.Start:sp.End],
			Confidence: sp.Conf,
			Reason:     sp.Reason,
		}
		// Вид маскирования сохраняет число рун, поэтому фрагмент маски лежит
		// по тем же рунным границам. Для видов, меняющих длину, показываем
		// весь результат целиком и поле оставляем пустым.
		if len(maskedRunes) == len(origRunes) && item.EndRune <= len(maskedRunes) {
			item.Masked = string(maskedRunes[item.StartRune:item.EndRune])
		}
		out = append(out, item)
	}
	return out
}

// inspectSkippedSpans переводит снятые фрагменты в форму ответа разбора.
// Границы проверяются ещё раз: снятый фрагмент приходит от правила, которое
// его отвергло, и вырезать по ним кусок текста можно только убедившись, что
// они лежат внутри присланного текста.
func inspectSkippedSpans(text string, skipped []pii.Span) []inspectSkipped {
	out := make([]inspectSkipped, 0, len(skipped))
	for _, sp := range skipped {
		if sp.Start < 0 || sp.End > len(text) || sp.Start >= sp.End {
			continue
		}
		out = append(out, inspectSkipped{
			Type:       string(sp.Type),
			Start:      sp.Start,
			End:        sp.End,
			Value:      text[sp.Start:sp.End],
			Confidence: sp.Conf,
			Reason:     sp.Reason,
		})
	}
	return out
}

// inspectEdges переводит связи между фрагментами в форму ответа разбора.
func inspectEdges(edges []engine.Edge) []inspectEdge {
	out := make([]inspectEdge, 0, len(edges))
	for _, e := range edges {
		out = append(out, inspectEdge{
			A:          e.A,
			B:          e.B,
			Basis:      string(e.Basis),
			Confidence: e.Conf,
		})
	}
	return out
}

// inspectPlaceholders переводит подстановки в соответствие плейсхолдер →
// значение. Подстановок нет — карта не заводится вовсе, и поле из ответа
// пропадает: для видов маскирования без плейсхолдеров показывать нечего.
func inspectPlaceholders(placeholders []mask.Placeholder) map[string]string {
	if len(placeholders) == 0 {
		return nil
	}
	out := make(map[string]string, len(placeholders))
	for _, ph := range placeholders {
		out[ph.Token] = ph.Value
	}
	return out
}

// runeOffsets строит перевод байтового смещения в номер руны для каждой
// позиции текста, включая позицию за последним байтом.
func runeOffsets(text string) []int {
	idx := make([]int, len(text)+1)
	n := 0
	for i := range text {
		idx[i] = n
		n++
	}
	cur := 0
	for i := 0; i < len(text); i++ {
		if text[i]&0xC0 != 0x80 {
			cur = idx[i]
		} else {
			idx[i] = cur
		}
	}
	idx[len(text)] = n
	return idx
}
