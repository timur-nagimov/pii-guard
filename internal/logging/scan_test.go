package logging

import "testing"

// TestInspectStringCatchesPII проверяет главное свойство пакета: значения,
// похожие на персональные данные, до журнала не доходят. Значения в наборе
// вымышленные, они совпадают с примерами из описания формата маскирования.
func TestInspectStringCatchesPII(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"фио", "Иванов Иван Иванович"},
		{"фио из двух слов", "Иванов Иван"},
		{"дата рождения", "12.03.1985"},
		{"дата в обратном порядке", "1985-03-12"},
		{"паспорт", "4509 123456"},
		{"паспорт с разделяющими словами", "серия 4509 номер 123456"},
		{"код подразделения", "770-001"},
		{"индекс", "101000"},
		{"почта", "ivanov@mail.ru"},
		{"почта с точкой в имени", "i.ivanov@corp.example.ru"},
		{"телефон", "+7 (916) 123-45-67"},
		{"телефон без знаков", "79161234567"},
		{"инн", "500100732259"},
		{"номер карты", "4111 1111 1111 1111"},
		{"снилс", "112-233-445 95"},
		{"имя держателя карты", "IVAN IVANOV"},
		{"адрес", "г. Москва, ул. Ленина, д. 5, кв. 12"},
		{"водительское удостоверение", "77 АА 123456"},
		{"кусок текста вместо поля", string(make([]byte, maxValueBytes+1))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, bad := inspectString(c.value)
			if !bad {
				t.Fatalf("значение %q прошло проверку, а должно было быть задержано", c.value)
			}
			if reason == "" {
				t.Fatal("причина задержания не указана")
			}
		})
	}
}

// TestInspectStringKeepsServiceValues проверяет обратное свойство: собственные
// значения сервиса проверка не трогает. Ложное срабатывание не приводит к
// утечке, но делает журнал бесполезным, поэтому набор тоже обязателен.
func TestInspectStringKeepsServiceValues(t *testing.T) {
	values := []string{
		"обработан запрос",
		"настройки применены",
		"общее хранилище недоступно, работаем через память",
		"не удалось подготовить сертификат, HTTPS выключен",
		"dial tcp 10.129.0.22:6379: connect: connection refused",
		"84.201.166.35",
		"255.255.255.255",
		"0.0.0.0:8080",
		"/v1/chat/completions",
		"context deadline exceeded",
		"Client.Timeout exceeded while awaiting headers",
		"unexpected EOF",
		"open configs/config.yaml: no such file or directory",
		"mask_retry",
		"store_degraded",
		"application/json; charset=utf-8",
		"v1.24.1",
		"ключ должен быть длиной 32 байта",
		"GET POST PUT",
	}
	for _, v := range values {
		if reason, bad := inspectString(v); bad {
			t.Errorf("значение %q задержано по причине %q, хотя это собственное значение сервиса", v, reason)
		}
	}
}

func TestSafeID(t *testing.T) {
	good := []string{"abc123", "01HZX-9K", "trace:abc.def", "a"}
	for _, v := range good {
		if _, ok := SafeID(v); !ok {
			t.Errorf("идентификатор %q отвергнут", v)
		}
	}
	bad := []string{"", "Иванов Иван", "id with space", "id\nx", string(make([]byte, maxIDLen+1))}
	for _, v := range bad {
		if _, ok := SafeID(v); ok {
			t.Errorf("идентификатор %q принят, хотя не должен", v)
		}
	}
}
