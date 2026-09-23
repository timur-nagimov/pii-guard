package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestSumRingWindow(t *testing.T) {
	r := newSumRing(3)
	r.add(100, 1)
	r.add(101, 2)
	r.add(102, 3)
	if got := r.sum(102); got != 6 {
		t.Fatalf("сумма окна: %v", got)
	}
	r.add(103, 4)
	if got := r.sum(103); got != 9 {
		t.Fatalf("после сдвига: %v", got)
	}
	if got := r.sum(200); got != 0 {
		t.Fatalf("после длинной паузы: %v", got)
	}
	r.add(200, 5)
	if got := r.sum(200); got != 5 {
		t.Fatalf("после очистки: %v", got)
	}
	if got := r.sum(150); got != 5 {
		t.Fatalf("часы назад: %v", got)
	}
}

func TestQuantileRing(t *testing.T) {
	r := newQuantileRing(60, queueWaitBuckets)
	if got := r.quantile(10, 0.95); got != 0 {
		t.Fatalf("пустое окно: %v", got)
	}
	for i := 0; i < 95; i++ {
		r.observe(10, 0.00005)
	}
	for i := 0; i < 5; i++ {
		r.observe(10, 0.2)
	}
	got := r.quantile(10, 0.95)
	if got < 0.00001 || got > 0.0001 {
		t.Fatalf("доля 95 вне первого наблюдения: %v", got)
	}
	got = r.quantile(10, 0.99)
	if got < 0.1 || got > 0.25 {
		t.Fatalf("доля 99: %v", got)
	}
	for i := 0; i < 10; i++ {
		r.observe(11, 5)
	}
	if got := r.quantile(11, 0.999); got != 1 {
		t.Fatalf("переполнение верхней границы: %v", got)
	}
	if got := r.quantile(500, 0.95); got != 0 {
		t.Fatalf("окно должно было протухнуть: %v", got)
	}
}

func TestCapacityRatioModel(t *testing.T) {
	if c := modelCoreMillis(250); math.Abs(c-0.9508) > 0.001 {
		t.Fatalf("стоимость 250 байт: %v", c)
	}
	if c := modelCoreMillis(2048); math.Abs(c-3.69) > 0.01 {
		t.Fatalf("стоимость 2 КБ: %v", c)
	}
	if c := modelCoreMillis(8192); math.Abs(c-23.49) > 0.01 {
		t.Fatalf("стоимость 8 КБ: %v", c)
	}
	m := New()
	m.coresUsable = 1
	if got := m.capacityUsedRatio(); got != 0 {
		t.Fatalf("без нагрузки: %v", got)
	}
	// Одно ядро, окно 15 секунд, запросы по 2 КБ: 1000 запросов в секунду
	// заняли бы 3.69 ядра, то есть при одном ядре занятость 3.69.
	for i := 0; i < 1000; i++ {
		m.loadRing.add(1000, modelCoreMillis(2048))
	}
	want := 1000 * 3.69 / (1 * 15 * 1000)
	if got := m.loadRing.sum(1000) / (m.coresUsable * 15 * 1000); math.Abs(got-want) > 0.01 {
		t.Fatalf("занятость: %v, ожидали %v", got, want)
	}
}

// TestNewRepeated проверяет, что повторный вызов New не паникует: каждый набор
// показателей живёт в своём реестре.
func TestNewRepeated(t *testing.T) {
	for i := 0; i < 3; i++ {
		m := New()
		if m == nil {
			t.Fatal("New вернул пустой набор")
		}
	}
}

// TestEstimateTokens проверяет оценку числа токенов по размеру текста.
func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(0); got != 0 {
		t.Fatalf("пустой текст: %v", got)
	}
	if got := EstimateTokens(-5); got != 0 {
		t.Fatalf("отрицательный размер: %v", got)
	}
	if got := EstimateTokens(280); math.Abs(got-100) > 0.001 {
		t.Fatalf("280 байт: %v", got)
	}
}

// TestObserveProcessLabels проверяет, что метки показателей берутся из
// закрытого набора и не порождаются из пользовательского ввода.
func TestObserveProcessLabels(t *testing.T) {
	m := New()
	m.ObserveProcess("система", "process", time.Millisecond, 100, map[string]int{"FIO": 2})
	m.ObserveStatus("система", "process", "429")
	m.ObserveSkipped("FIO", "public_figure")
	m.ObserveDegraded("система")
	m.ObserveAmbiguous("система")
	m.ObservePanic("FIO")
	m.ObserveUpstream("система", "200", time.Millisecond)
	m.ObserveQueueWait(time.Millisecond)
	m.IncInflight()
	m.DecInflight()
	m.SetRecords(5)
	// Проверяем, что показатели отдаются без ошибок.
	_ = m.Handler()
}

// TestRegisterCollector проверяет добавление стороннего сборщика.
func TestRegisterCollector(t *testing.T) {
	m := New()
	// Регистрация сборщика, уже зарегистрированного в реестре, даёт ошибку:
	// реестр не принимает дубликаты.
	dup := prometheus.NewCounter(prometheus.CounterOpts{Name: "pii_dup_test"})
	if err := m.Register(dup); err != nil {
		t.Fatalf("новый сборщик не зарегистрирован: %v", err)
	}
	if err := m.Register(dup); err == nil {
		t.Fatal("повторная регистрация не дала ошибку")
	}
}
