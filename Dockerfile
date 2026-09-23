# Сборка статического исполняемого файла.
# Образ закреплён дайджестом: тег 1.26-alpine перемещают на новый слепок при
# каждом обновлении, и без дайджеста одна и та же ревизия исходников со
# временем собиралась бы разными компиляторами — повторить сборку, которую
# замеряли, было бы нечем.
FROM golang:1.26-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c AS build
# Метку несут оба образа: метки не наследуются между стадиями, а сборочная
# стадия остаётся в локальном кеше отдельным слепком, и по ней тоже должно
# читаться, чей это образ.
LABEL maintainer="Команда pii-guard"
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pii-guard ./cmd/pii-guard

# Итоговый образ без оболочки и пакетного менеджера: меньше поверхность атаки.
# Дайджест здесь важнее, чем на сборочном образе: именно этот слепок уезжает
# на стенд, и по нему же потом сверяют, что там работает ровно то, что собрали.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
# В distroless нет оболочки, чтобы осмотреться внутри, поэтому владелец
# итогового образа должен читаться снаружи, из метки.
LABEL maintainer="Команда pii-guard"
WORKDIR /app
COPY --from=build /out/pii-guard /app/pii-guard
COPY configs/config.yaml /app/configs/config.yaml
USER nonroot:nonroot
EXPOSE 8080 8443
# В образе нет curl и wget, поэтому живость проверяет сам исполняемый файл.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/app/pii-guard", "-healthcheck"]
ENTRYPOINT ["/app/pii-guard"]
CMD ["-config", "/app/configs/config.yaml"]
