package pii

import "sort"

// Registry — набор зарегистрированных детекторов. Пополняется при старте
// сервиса и дальше только читается, поэтому безопасен для параллельных
// запросов без блокировок.
//
// Новый тип персональных данных добавляется регистрацией ещё одного детектора
// или записью в раздел custom_types конфигурации — ядро при этом не меняется.
type Registry struct {
	detectors []Detector
}

// NewRegistry создаёт пустой набор детекторов.
func NewRegistry() *Registry { return &Registry{} }

// Register добавляет детектор в набор.
func (r *Registry) Register(d ...Detector) {
	r.detectors = append(r.detectors, d...)
}

// Detectors возвращает зарегистрированные детекторы.
func (r *Registry) Detectors() []Detector { return r.detectors }

// Types перечисляет все типы, которые умеет находить набор.
func (r *Registry) Types() []Type {
	seen := make(map[Type]bool)
	var out []Type
	for _, d := range r.detectors {
		for _, t := range d.Types() {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// Detect прогоняет все детекторы по документу и возвращает объединённый список
// фрагментов, отсортированный по началу, без разрешения пересечений.
// Пересечения снимает Resolve.
func (r *Registry) Detect(d *Doc) []Span {
	var spans []Span
	for _, det := range r.detectors {
		spans = append(spans, det.Detect(d)...)
	}
	SortSpans(spans)
	return spans
}

// SortSpans упорядочивает фрагменты по началу, затем по убыванию длины,
// затем по убыванию уверенности — в таком порядке их разбирает Resolve.
func SortSpans(spans []Span) {
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		if spans[i].Len() != spans[j].Len() {
			return spans[i].Len() > spans[j].Len()
		}
		return spans[i].Conf > spans[j].Conf
	})
}

// typePriority задаёт, какой тип побеждает при равной длине пересекающихся
// фрагментов. Чем больше число, тем выше приоритет.
var typePriority = map[Type]int{
	TypeCard:            100,
	TypeSNILS:           95,
	TypeINN:             90,
	TypePhone:           85,
	TypePassport:        80,
	TypeForeignPassport: 78,
	TypeDriverLicense:   75,
	TypeResidencePermit: 72,
	TypeBirthCert:       70,
	TypeMilitaryID:      68,
	TypeDeptCode:        65,
	TypeDOB:             60,
	TypeIssueDate:       58,
	TypePostcode:        50,
	TypeAddress:         45,
	TypeIssuer:          42,
	TypeBirthPlace:      40,
	TypeFIO:             35,
	TypeCardHolder:      30,
	TypeCitizenship:     25,
	TypeEmail:           20,
	TypeCVV:             15,
	TypePIN:             14,
}

// Priority возвращает приоритет типа при разрешении пересечений.
func Priority(t Type) int { return typePriority[t] }

// Resolve снимает пересечения фрагментов. Правила по порядку:
// отбрасываются фрагменты с уверенностью ниже порога; из пересекающихся
// побеждает более длинный; при равной длине — более приоритетный тип;
// при равном приоритете — более уверенный.
//
// Второй результат — отклонённые фрагменты, они нужны для журнала и метрик.
func Resolve(spans []Span, minConf float64) (kept, dropped []Span) {
	SortSpans(spans)
	for _, s := range spans {
		if !s.Valid() {
			continue
		}
		if s.Conf < minConf {
			s.Reason = appendReason(s.Reason, "below_min_confidence")
			dropped = append(dropped, s)
			continue
		}
		conflict := -1
		for i := range kept {
			if kept[i].Overlaps(s) {
				conflict = i
				break
			}
		}
		if conflict < 0 {
			kept = append(kept, s)
			continue
		}
		if betterSpan(s, kept[conflict]) {
			loser := kept[conflict]
			loser.Reason = appendReason(loser.Reason, "overlap_lost")
			dropped = append(dropped, loser)
			kept[conflict] = s
		} else {
			s.Reason = appendReason(s.Reason, "overlap_lost")
			dropped = append(dropped, s)
		}
	}
	SortSpans(kept)
	return kept, dropped
}

// betterSpan сообщает, что фрагмент a должен вытеснить фрагмент b.
func betterSpan(a, b Span) bool {
	if a.Len() != b.Len() {
		return a.Len() > b.Len()
	}
	if pa, pb := Priority(a.Type), Priority(b.Type); pa != pb {
		return pa > pb
	}
	return a.Conf > b.Conf
}

func appendReason(reason, add string) string {
	if reason == "" {
		return add
	}
	return reason + "+" + add
}
