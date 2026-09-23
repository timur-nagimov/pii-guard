package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync/atomic"
)

// Пороги проверки значений. Подобраны так, чтобы поймать все формы из раздела
// типов персональных данных и при этом не задевать наши собственные поля:
// адреса, версии, коды ответов, имена событий.
const (
	// minSuspectLen — короче этого значение не разбирается: в трёх-четырёх
	// знаках нет опознаваемых данных.
	minSuspectLen = 6
	// maxValueBytes — предел длины значения поля. Ни одно поле журнала не
	// обязано быть длиннее; всё, что длиннее, это почти наверняка кусок
	// входного текста, а значит потенциальная утечка.
	maxValueBytes = 512
	// digitRunLimit — длина сплошной цепочки цифр: паспорт, индекс, ИНН.
	digitRunLimit = 6
	// digitGroupLimit — число цифр в группе, разделённой знаками: телефон,
	// номер карты, СНИЛС, код подразделения.
	digitGroupLimit = 6
	// nameWordsLimit — сколько слов с прописной кириллической буквы в одном
	// значении считать собственным именем.
	nameWordsLimit = 2
	// nameWordLen — минимальная длина такого слова.
	nameWordLen = 3
	// upperWordLen — минимальная длина слова из прописных латинских букв.
	// Три знака это сокращения вроде GET и TLS, четыре и больше уже похоже на
	// имя держателя карты.
	upperWordLen = 4
	// upperNameLen — минимальная длина строки из одних прописных латинских
	// букв, чтобы счесть её именем держателя карты.
	upperNameLen = 9
	// maxGroupDepth — глубина разбора вложенных групп полей.
	maxGroupDepth = 4
	// idDigitLimit — сколько цифр в значении, состоящем из одних цифр и
	// разделителей, делает его непохожим на идентификатор и похожим на номер
	// карты, телефон, СНИЛС или ИНН. Шесть, как и у цепочки цифр в тексте.
	idDigitLimit = 6
	// idHashLen — сколько байтов хеша оставляет политика идентификатора.
	// Восемь байтов это шестнадцать знаков в записи и запас по столкновениям
	// на всём потоке журнала.
	idHashLen = 8
)

// idHashPrefix — приставка у идентификатора, заменённого отпечатком. По ней
// видно, что в поле не то, что прислал клиент, и что сравнивать это значение
// надо с отпечатком, а не с исходной строкой.
const idHashPrefix = "hash:"

// Причины, по которым значение не попало в журнал.
const (
	reasonTooLong  = "too_long"
	reasonDigitRun = "digit_run"
	reasonDigits   = "digit_group"
	reasonEmail    = "email"
	reasonName     = "person_name"
	reasonUpper    = "upper_name"
)

// trusted сообщает, что значение поля сервис формирует сам из закрытого
// набора: имена событий, коды, пути, адреса. Такие поля не разбираются, и это
// главная экономия на горячем пути.
//
// Выбор в пользу переключателя, а не карты, сделан по измерению: переключатель
// по строкам компилируется в сравнение по длине и содержимому и обходится
// дешевле вычисления хеша на каждое поле каждой записи.
//
// Полей request_id и payload_id здесь нет намеренно: их значение приходит от
// клиента. У них своя проверка, строже доверия и мягче разбора значений, —
// idField и LogID ниже.
func trusted(key string) bool {
	switch key {
	case FieldEvent, FieldSystem, FieldComponent, FieldOp,
		FieldStatus, FieldMethod, FieldPath, FieldStream,
		FieldService, FieldInstance, FieldVersion, FieldDuration, FieldBytes,
		"level", "type", "reason", "direction", "code", "preset", "addr",
		"from", "to", "env", "result":
		return true
	default:
		return false
	}
}

// idField сообщает, что поле несёт идентификатор запроса или обрабатываемого
// текста. Значение такого поля приходит от клиента, поэтому доверять ему
// нельзя; но и разбирать его наравне с остальными нельзя тоже: по
// идентификатору сходятся записи одного запроса, и вычищенный идентификатор
// рвёт связность журнала. Для таких полей действует своя политика, LogID.
func idField(key string) bool {
	return key == FieldPayloadID || key == FieldRequestID
}

// LogID применяет политику идентификатора: пригодный на вид идентификатор
// записывается как есть, непригодный заменяется отпечатком.
//
// Почему отпечаток, а не вычистка и не обрезка:
//
//   - вычистить целиком нельзя: по идентификатору сходятся записи одного
//     запроса, без него журнал перестаёт быть связным, а запись аудита
//     перестаёт отвечать на вопрос «какое обращение это было»;
//   - оставить первые знаки нельзя: первые шесть знаков номера карты это
//     банк и платёжная система, первые знаки телефона это код оператора и
//     региона, то есть в журнале остаётся кусок тех самых данных;
//   - отпечаток сохраняет главное свойство идентификатора — одинаковые
//     значения дают одинаковый отпечаток, поэтому записи одного запроса
//     по-прежнему сходятся, а разные запросы по-прежнему различимы.
//
// Цена решения: по журналу нельзя найти запрос по присланному клиентом
// идентификатору напрямую, нужно сперва посчитать от него отпечаток. Это
// касается только тех идентификаторов, которые на идентификатор не похожи;
// для нормальных значение в журнале остаётся дословным.
func LogID(s string) string {
	if s == "" || idPlausible(s) {
		return s
	}
	return hashID(s)
}

// idPlausible сообщает, что строка похожа на идентификатор, а не на значение
// персональных данных.
//
// Законным считается значение не длиннее предела, из букв, цифр и знаков
// -_.:, в котором есть хотя бы одна буква либо меньше шести цифр. Пример
// идентификатора из задания — шестнадцатеричная строка из тридцати двух
// знаков — проходит: строка без единой буквы встречается примерно раз на три
// миллиона. Длинная строка из одних цифр не проходит: на
// идентификатор она не похожа, зато похожа на номер карты, телефон, СНИЛС
// или ИНН.
func idPlausible(s string) bool {
	if len(s) > maxIDLen {
		return false
	}
	digits, letters := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letters = true
		case c == '-', c == '_', c == '.', c == ':':
		default:
			// Пробел, собачка, плюс, кириллица: разметка чужой строки, а не
			// идентификатор. Так сюда попадают почта и телефон с плюсом.
			return false
		}
	}
	return letters || digits < idDigitLimit
}

// hashID возвращает отпечаток идентификатора. Приставка гарантирует, что в
// отпечатке есть буквы, поэтому повторное применение политики его не трогает.
func hashID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return idHashPrefix + hex.EncodeToString(sum[:idHashLen])
}

// Stats — счётчики работы журнала. Нужны, чтобы видеть, что защита от утечки
// срабатывает, и чтобы понимать, сколько записей проглочено глушителем.
type Stats struct {
	// Records — сколько записей дошло до проверки.
	Records atomic.Uint64
	// Redactions — сколько значений вычищено защитой от утечки.
	Redactions atomic.Uint64
	// IDReplaced — сколько идентификаторов заменено отпечатком. Считается
	// отдельно от Redactions намеренно: вычищенное значение означает ошибку в
	// нашем коде, а непригодный идентификатор присылает клиент, и тревожить
	// этим дежурного не за что.
	IDReplaced atomic.Uint64
	// Suppressed — сколько повторов заглушено.
	Suppressed atomic.Uint64
	// Sampled — сколько записей об успешных запросах отброшено прореживанием.
	Sampled atomic.Uint64
}

// required — какие обязательные поля в записи уже есть. Набор собирается за
// тот же проход по полям, что и проверка значений, и нужен затем, чтобы
// обработчик не дописывал поле, которое место вызова передало само: два
// ключа с одним именем в JSON это запись, которую разбирают по-разному
// разные читатели, а чаще не разбирают вовсе.
type required struct {
	event     bool
	duration  bool
	requestID bool
	system    bool
}

// note отмечает поле записи в наборе.
func (rq *required) note(key string) {
	switch key {
	case FieldEvent:
		rq.event = true
	case FieldDuration:
		rq.duration = true
	case FieldRequestID:
		rq.requestID = true
	case FieldSystem:
		rq.system = true
	default:
		// Прочие поля обязательными не считаются: набор нужен ровно затем,
		// чтобы не дописать второй ключ поверх уже переданного.
	}
}

// guard — обработчик, который стоит перед выводом и делает три вещи за один
// проход по полям: добавляет обязательные поля, достаёт идентификатор запроса
// и систему из контекста, вычищает значения, похожие на персональные данные.
type guard struct {
	next   slog.Handler
	redact bool
	stats  *Stats

	// have запоминает обязательные поля, пришедшие через WithAttrs. Такие
	// поля разбираются один раз при создании журнала, а не на каждой записи.
	have required
}

func newGuard(next slog.Handler, redact bool, stats *Stats) *guard {
	return &guard{next: next, redact: redact, stats: stats}
}

func (g *guard) Enabled(ctx context.Context, l slog.Level) bool { return g.next.Enabled(ctx, l) }

func (g *guard) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *g
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out.have.note(a.Key)
		cleaned = append(cleaned, g.clean(a, 0))
	}
	out.next = g.next.WithAttrs(cleaned)
	return &out
}

func (g *guard) WithGroup(name string) slog.Handler {
	out := *g
	out.next = g.next.WithGroup(name)
	return &out
}

// Handle проверяет запись и передаёт её дальше.
func (g *guard) Handle(ctx context.Context, r slog.Record) error {
	g.stats.Records.Add(1)

	have, dirty := g.inspect(r)
	msg, msgReason := g.checkMessage(r.Message)

	if !dirty && msgReason == "" {
		// Ровный путь: поля чистые, менять нужно только состав. Клонирование
		// записи дешевле, чем сборка новой с переносом полей по одному.
		if have.event && have.duration && !g.needContext(ctx, have) {
			return g.next.Handle(ctx, r)
		}
		out := r.Clone()
		g.addRequired(ctx, &out, have)
		return g.next.Handle(ctx, out)
	}

	out := slog.NewRecord(r.Time, r.Level, msg, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(g.clean(a, 0))
		return true
	})
	if msgReason != "" {
		out.AddAttrs(slog.String(FieldRedacted, msgReason))
	}
	g.addRequired(ctx, &out, have)
	return g.next.Handle(ctx, out)
}

// needContext сообщает, есть ли в контексте данные запроса, которых нет в
// самой записи. Поле, переданное местом вызова, из контекста не дописывается:
// иначе в JSON вышло бы два ключа с одним именем.
func (g *guard) needContext(ctx context.Context, have required) bool {
	if have.requestID && have.system {
		return false
	}
	id, sys := reqFields(ctx)
	return (!have.requestID && id != "") || (!have.system && sys != "")
}

// reqFields достаёт идентификатор запроса и систему одним обращением к
// контексту. Раздельные RequestID и System искали бы одно и то же значение
// дважды на каждой записи.
func reqFields(ctx context.Context) (id, system string) {
	if ctx == nil {
		return "", ""
	}
	info, _ := ctx.Value(ctxKey{}).(*reqInfo)
	if info == nil {
		return "", ""
	}
	if p := info.system.Load(); p != nil {
		system = *p
	}
	return info.id, system
}

// addRequired дописывает обязательные поля, которых не оказалось в записи.
// Стандарт полей держится здесь, а не на дисциплине тридцати мест вызова.
//
// Дописывается только то, чего в записи нет. Место вызова, передавшее
// request_id или system явно, получает свою запись без второго ключа с тем же
// именем: набор have собран проходом по полям в inspect и WithAttrs.
func (g *guard) addRequired(ctx context.Context, out *slog.Record, have required) {
	id, sys := reqFields(ctx)
	if !have.requestID && id != "" {
		// Идентификатор из контекста проходит ту же политику, что и поле
		// записи: в контекст он попадает из заголовка запроса, то есть от
		// клиента, и точно так же может оказаться номером карты.
		out.AddAttrs(slog.String(FieldRequestID, g.safeID(id)))
	}
	if !have.system && sys != "" {
		out.AddAttrs(slog.String(FieldSystem, sys))
	}
	if !have.event {
		out.AddAttrs(slog.String(FieldEvent, EventUnspecified))
	}
	if !have.duration {
		out.AddAttrs(slog.Float64(FieldDuration, 0))
	}
}

// safeID применяет политику идентификатора и считает срабатывание.
func (g *guard) safeID(s string) string {
	if !g.redact || idPlausible(s) {
		return s
	}
	g.stats.IDReplaced.Add(1)
	return hashID(s)
}

// inspect проходит по полям записи один раз: смотрит, есть ли обязательные
// поля и есть ли значения, которые придётся вычистить.
func (g *guard) inspect(r slog.Record) (have required, dirty bool) {
	have = g.have
	r.Attrs(func(a slog.Attr) bool {
		have.note(a.Key)
		if g.redact && !dirty && g.suspect(a, 0) {
			dirty = true
		}
		return true
	})
	return have, dirty
}

// checkMessage проверяет само сообщение. Сообщение обязано быть константой,
// поэтому срабатывание проверки здесь означает ошибку в коде: значение
// склеили с текстом.
func (g *guard) checkMessage(msg string) (string, string) {
	if !g.redact {
		return msg, ""
	}
	reason, bad := inspectString(msg)
	if !bad {
		return msg, ""
	}
	g.stats.Redactions.Add(1)
	return "сообщение удалено: подозрение на персональные данные", reason
}

// suspect сообщает, требует ли поле вычистки.
func (g *guard) suspect(a slog.Attr, depth int) bool {
	if idField(a.Key) {
		v := a.Value.Resolve()
		return v.Kind() == slog.KindString && !idPlausible(v.String())
	}
	if trusted(a.Key) {
		return false
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		_, bad := inspectString(v.String())
		return bad
	case slog.KindGroup:
		return g.suspectGroup(v, depth)
	case slog.KindAny:
		_, bad := inspectString(anyToString(v))
		return bad
	default:
		// Числа, признаки, длительности и время не могут нести значение
		// персональных данных в опознаваемом виде.
		return false
	}
}

// clean возвращает поле, пригодное к записи: подозрительное значение
// заменяется пометкой с причиной и длиной, само значение не сохраняется.
func (g *guard) clean(a slog.Attr, depth int) slog.Attr {
	if !g.redact {
		return a
	}
	if idField(a.Key) {
		return g.cleanID(a)
	}
	if trusted(a.Key) {
		return a
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		if reason, bad := inspectString(v.String()); bad {
			return g.mark(a.Key, reason, len(v.String()))
		}
		return slog.Attr{Key: a.Key, Value: v}
	case slog.KindGroup:
		return g.cleanGroup(a.Key, v, depth)
	case slog.KindAny:
		s := anyToString(v)
		if reason, bad := inspectString(s); bad {
			return g.mark(a.Key, reason, len(s))
		}
		return slog.Attr{Key: a.Key, Value: v}
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// suspectGroup проверяет вложенную группу полей. Слишком глубокая группа
// считается подозрительной целиком: разобрать её до дна дороже, чем вычистить.
func (g *guard) suspectGroup(v slog.Value, depth int) bool {
	if depth >= maxGroupDepth {
		return true
	}
	for _, sub := range v.Group() {
		if g.suspect(sub, depth+1) {
			return true
		}
	}
	return false
}

// cleanGroup вычищает вложенную группу поле за полем. Слишком глубокая группа
// заменяется пометкой целиком: спускаться дальше дороже, чем потерять её.
func (g *guard) cleanGroup(key string, v slog.Value, depth int) slog.Attr {
	if depth >= maxGroupDepth {
		return g.mark(key, "too_deep", 0)
	}
	src := v.Group()
	out := make([]slog.Attr, 0, len(src))
	for _, sub := range src {
		out = append(out, g.clean(sub, depth+1))
	}
	return slog.Attr{Key: key, Value: slog.GroupValue(out...)}
}

// cleanID применяет к полю идентификатора политику LogID. Значение не
// вычищается: связность журнала по идентификатору важнее, отпечаток её
// сохраняет.
func (g *guard) cleanID(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() != slog.KindString {
		return slog.Attr{Key: a.Key, Value: v}
	}
	s := v.String()
	if idPlausible(s) {
		return slog.Attr{Key: a.Key, Value: v}
	}
	g.stats.IDReplaced.Add(1)
	return slog.String(a.Key, hashID(s))
}

func (g *guard) mark(key, reason string, n int) slog.Attr {
	g.stats.Redactions.Add(1)
	return slog.String(key, "[удалено: "+reason+", длина "+strconv.Itoa(n)+"]")
}

// anyToString приводит произвольное значение к строке для проверки. Разбору
// подлежат только те виды, которые способны нести текст: числа, ошибки и
// структуры превращать в строку на горячем пути дорого и не нужно.
func anyToString(v slog.Value) string {
	switch x := v.Any().(type) {
	case string:
		return x
	case error:
		return x.Error()
	case []byte:
		return string(x)
	case []string:
		// Список строк встречается в местах вызова: список типов, список
		// причин. Проверяем каждый элемент по отдельности.
		return firstSuspectString(x)
	default:
		return ""
	}
}

// firstSuspectString возвращает первый подозрительный элемент списка. Пустая
// строка означает, что подозрительных нет: она всё равно проверку проходит.
func firstSuspectString(list []string) string {
	for _, s := range list {
		if _, bad := inspectString(s); bad {
			return s
		}
	}
	return ""
}
