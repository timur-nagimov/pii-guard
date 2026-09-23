// Отдельный вход одного отправителя держим в тестовом файле: прогон собирает
// loadRun целиком сам, и вне проверки одного отправителя этот вход не зовёт
// никто. В рабочих исходниках он только повторял бы поля loadRun по отдельности.

package main

import (
	"context"
	"net/http"
	"time"
)

// runWorker гоняет запросы одного отправителя, пока жив контекст, и собирает
// задержки успешных ответов. Возвращает список задержек для этого отправителя.
// Отдельный вход без сборки прогона целиком нужен тесту одного отправителя.
func runWorker(ctx context.Context, client *http.Client, target string, texts []string, interval time.Duration, seed uint64, id int, c *counters) []time.Duration {
	l := &loadRun{
		client:   client,
		target:   target,
		texts:    texts,
		interval: interval,
		seed:     seed,
		c:        c,
	}
	return l.worker(ctx, id)
}
