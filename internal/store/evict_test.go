package store

import (
	"fmt"
	"testing"
	"time"
)

// TestEvictOnlyOneOverLimit проверяет, что превышение предела на одну запись
// вытесняет ровно одну запись, а не по одной из каждого сегмента. Раньше
// вытеснение шло по всем сегментам сразу, и один запрос сверх предела уносил
// до двухсот пятидесяти шести действующих записей.
func TestEvictOnlyOneOverLimit(t *testing.T) {
	const max = 10
	s := newTestStore(t, Config{TTL: time.Hour, MaxRecords: max})
	for i := 0; i < max; i++ {
		if _, err := s.Put(fmt.Sprintf("id-%d", i), "текст", "маска", "sys", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.Len(); n != max {
		t.Fatalf("до превышения предела в хранилище %d записей, ожидалось %d", n, max)
	}
	if _, err := s.Put("id-over", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	if n := s.Len(); n != max {
		t.Fatalf("после превышения предела на одну запись в хранилище %d записей, ожидалось %d (вытеснено слишком много)", n, max)
	}
}
