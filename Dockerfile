# Сборка статического исполняемого файла.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pii-guard ./cmd/pii-guard

# Итоговый образ без оболочки и пакетного менеджера: меньше поверхность атаки.
FROM gcr.io/distroless/static-debian12:nonroot
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
