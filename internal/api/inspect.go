package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"pii-guard/internal/logging"
	"pii-guard/internal/mask"
)

// inspectRequest — тело запроса на разбор текста.
type inspectRequest struct {
	// Text — текст для разбора.
	Text string `json:"text"`
	// Preset переопределяет вид маскирования только для этого запроса.
	// Пустое значение означает вид, заданный системе в настройках.
	Preset string `json:"preset,omitempty"`
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
	sys, ok := s.resolveSystem(r, cfg)
	if !ok {
		s.writeError(w, r, http.StatusForbidden, "system_not_allowed", "система не опознана или отключена")
		return
	}
	if !sys.InspectOn(cfg.Defaults) {
		s.writeError(w, r, http.StatusForbidden, "inspect_disabled", "разбор текста для этой системы выключен настройкой")
		return
	}

	var req inspectRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, cfg.Server.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		s.writeValidation(w, r, "json_invalid", []string{"body"}, "не удалось разобрать JSON")
		return
	}
	if req.Text == "" {
		s.writeValidation(w, r, "missing", []string{"body", "text"}, "поле text обязательно и не может быть пустым")
		return
	}

	// Разовое переопределение вида маскирования: удобно сравнивать виды на
	// одном и том же тексте, не трогая настройки.
	if p := mask.Preset(req.Preset); req.Preset != "" {
		if !p.Valid() {
			s.writeValidation(w, r, "value_error", []string{"body", "preset"}, "неизвестный вид маскирования")
			return
		}
		sys.Preset = p
		sys.PerType = nil
	}

	started := time.Now()
	res := s.engine.Mask(req.Text, sys, cfg.Defaults)
	took := time.Since(started)

	runeAt := runeOffsets(req.Text)
	out := inspectResponse{
		System:    sys.Name,
		Preset:    string(sys.MaskOptions(cfg.Defaults).Default),
		Masked:    res.Text,
		Counts:    countsToStrings(res),
		TookMs:    float64(took.Microseconds()) / 1000,
		TextBytes: len(req.Text),
		TextRunes: len([]rune(req.Text)),
		Spans:     make([]inspectSpan, 0, len(res.Spans)),
		Skipped:   make([]inspectSkipped, 0, len(res.Skipped)),
	}

	maskedRunes := []rune(res.Text)
	origRunes := []rune(req.Text)
	for _, sp := range res.Spans {
		item := inspectSpan{
			Type:       string(sp.Type),
			Start:      sp.Start,
			End:        sp.End,
			StartRune:  runeAt[sp.Start],
			EndRune:    runeAt[sp.End],
			Value:      req.Text[sp.Start:sp.End],
			Confidence: sp.Conf,
			Reason:     sp.Reason,
		}
		// Вид маскирования сохраняет число рун, поэтому фрагмент маски лежит
		// по тем же рунным границам. Для видов, меняющих длину, показываем
		// весь результат целиком и поле оставляем пустым.
		if len(maskedRunes) == len(origRunes) && item.EndRune <= len(maskedRunes) {
			item.Masked = string(maskedRunes[item.StartRune:item.EndRune])
		}
		out.Spans = append(out.Spans, item)
	}
	for _, sp := range res.Skipped {
		if sp.Start < 0 || sp.End > len(req.Text) || sp.Start >= sp.End {
			continue
		}
		out.Skipped = append(out.Skipped, inspectSkipped{
			Type:       string(sp.Type),
			Start:      sp.Start,
			End:        sp.End,
			Value:      req.Text[sp.Start:sp.End],
			Confidence: sp.Conf,
			Reason:     sp.Reason,
		})
	}
	if len(res.Placeholders) > 0 {
		out.Placeholders = make(map[string]string, len(res.Placeholders))
		for _, ph := range res.Placeholders {
			out.Placeholders[ph.Token] = ph.Value
		}
	}

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
	s.auditProcess(r, sys, "", "inspect", out.TextBytes, out.Counts, took, "ok")
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
