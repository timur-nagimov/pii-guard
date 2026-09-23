package pii

import (
	"sort"
	"sync/atomic"
)

// Registry — набор зарегистрированных детекторов. Встроенные детекторы
// добавляются при старте сервиса и дальше только читаются, поэтому обходятся
// без блокировок.
//
// Детектор типов из настроек стоит особняком, в сменном слоте: его
// пересобирают при каждом перечитывании настроек, пока сервис обслуживает
// запросы. Замена атомарная, см. поле custom.
//
// Новый тип персональных данных добавляется регистрацией ещё одного детектора
// или записью в раздел custom_types конфигурации — ядро при этом не меняется.
type Registry struct {
	detectors []Detector

	// custom — детектор типов из настроек, вынесенный в отдельный сменный
	// слот. Остальные детекторы встроены в сборку и после запуска не
	// меняются, а этот пересобирается при каждом перечитывании настроек:
	// иначе добавленный тип не действовал бы до перезапуска сервиса.
	//
	// Замена атомарная, потому что читают слот на горячем пути, из всех
	// обработчиков сразу, а меняет его отдельная горутина наблюдения за
	// файлом. Хранится указатель на интерфейс, а не сам интерфейс: пустое
	// значение должно отличаться от «детектора нет».
	custom atomic.Pointer[Detector]

	onPanic PanicHandler
}

// NewRegistry создаёт пустой набор детекторов.
func NewRegistry() *Registry { return &Registry{} }

// Register добавляет детектор в набор.
func (r *Registry) Register(d ...Detector) {
	r.detectors = append(r.detectors, d...)
}

// SetCustom подменяет детектор типов из настроек. Пустое значение убирает его.
//
// Вызывается при запуске и при каждом применении новых настроек. Уже идущие
// запросы дорабатывают прежним детектором: замена указателя их не касается.
func (r *Registry) SetCustom(d Detector) {
	if d == nil {
		r.custom.Store(nil)
		return
	}
	r.custom.Store(&d)
}

// Detectors возвращает зарегистрированные детекторы, включая сменный.
func (r *Registry) Detectors() []Detector {
	if c := r.custom.Load(); c != nil {
		return append(append([]Detector(nil), r.detectors...), *c)
	}
	return r.detectors
}

// Types перечисляет все типы, которые умеет находить набор.
func (r *Registry) Types() []Type {
	seen := make(map[Type]bool)
	var out []Type
	for _, d := range r.Detectors() {
		for _, t := range d.Types() {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// PanicHandler вызывается, когда детектор завершился сбоем. Нужен, чтобы
// сервис мог посчитать такие случаи, не завися от пакета показателей.
type PanicHandler func(types []Type, recovered any)

// OnPanic задаёт обработчик сбоя детектора.
func (r *Registry) OnPanic(h PanicHandler) { r.onPanic = h }

// Detect прогоняет все детекторы по документу и возвращает объединённый список
// фрагментов, отсортированный по началу, без разрешения пересечений.
// Пересечения снимает Resolve.
//
// Сбой одного детектора не роняет обработку целиком: его тип просто
// пропускается. Для проверяющей системы это принципиально, потому что пять
// подряд невалидных ответов останавливают весь прогон.
func (r *Registry) Detect(d *Doc) []Span {
	var spans []Span
	for _, det := range r.detectors {
		spans = append(spans, r.detectOne(det, d)...)
	}
	// Сменный слот обходится отдельно, а не через Detectors(): тот собирает
	// новый срез, и на горячем пути это была бы лишняя выделенная память на
	// каждый запрос.
	if c := r.custom.Load(); c != nil {
		spans = append(spans, r.detectOne(*c, d)...)
	}
	SortSpans(spans)
	return spans
}

// detectOne вызывает один детектор, перехватывая его сбой.
func (r *Registry) detectOne(det Detector, d *Doc) (found []Span) {
	defer func() {
		if rec := recover(); rec != nil {
			found = nil
			if r.onPanic != nil {
				r.onPanic(det.Types(), rec)
			}
		}
	}()
	return det.Detect(d)
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
	// Место рождения стоит выше адреса намеренно. Оно срабатывает только по
	// явному якорю «место рождения», тогда как адрес опознаётся по форме, и
	// на одном и том же тексте побеждал адрес: значение маскировалось, но
	// помечалось чужим типом. Обратного риска нет — на адресе регистрации
	// детектор места рождения молчит, ему нужен свой якорь.
	TypeBirthPlace:  46,
	TypeAddress:     45,
	TypeIssuer:      42,
	TypeFIO:         35,
	TypeCardHolder:  30,
	TypeCitizenship: 25,
	TypeEmail:       20,
	TypeCVV:         15,
	TypePIN:         14,
	TypeAccount:     55,
	TypeOMS:         54,
	TypePlate:       53,
	TypeVIN:         52,
	TypeIPAddress:   51,
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
