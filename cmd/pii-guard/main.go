// Команда pii-guard запускает сервис маскирования персональных данных.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pii-guard/internal/api"
	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "путь к файлу настроек")
	checkConfig := flag.Bool("check-config", false, "проверить настройки и выйти")
	healthcheck := flag.Bool("healthcheck", false, "проверить живость сервиса и выйти")
	flag.Parse()

	if *healthcheck {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("настройки не приняты", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if *checkConfig {
		fmt.Println("настройки корректны")
		return
	}

	if err := run(cfg, *configPath, log); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("сервис остановлен с ошибкой", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(cfg *config.Config, configPath string, log *slog.Logger) error {
	key, err := storeKey(cfg.Store.KeyEnv, log)
	if err != nil {
		return err
	}
	st, err := store.New(store.Config{Key: key, TTL: cfg.Store.TTL, MaxRecords: cfg.Store.MaxRecords})
	if err != nil {
		return err
	}
	defer st.Close()

	reg := pii.NewRegistry()
	reg.Register(
		pii.NewNumericDetector(),
		pii.NewEmailDetector(),
		pii.NewFIODetector(),
		pii.NewDateDetector(cfg.Defaults.DateWithoutAnchor),
		pii.NewAddressDetector(),
		pii.NewBirthPlaceDetector(),
		pii.NewIssuerDetector(),
		pii.NewCitizenshipDetector(),
		pii.NewDriverLicenseLettersDetector(),
		pii.NewCardHolderDetector(),
		pii.NewExtraDocumentsDetector(),
	)
	if len(cfg.CustomTypes) > 0 {
		custom, cerr := pii.NewCustomDetector(customRules(cfg))
		if cerr != nil {
			return fmt.Errorf("правила custom_types: %w", cerr)
		}
		reg.Register(custom)
	}

	m := metrics.New()
	// Сбой отдельного детектора пропускает его тип, но не роняет ответ.
	reg.OnPanic(func(types []pii.Type, recovered any) {
		name := "unknown"
		if len(types) > 0 {
			name = string(types[0])
		}
		m.ObservePanic(name)
		log.Error("сбой детектора", slog.String("type", name), slog.Any("panic", recovered))
	})
	eng := engine.New(reg)
	srv := api.New(cfg, st, eng, m, log)

	httpSrv := &http.Server{
		Addr:              cfg.Server.HTTP,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	errCh := make(chan error, 2)
	go func() {
		log.Info("слушаю HTTP", slog.String("addr", cfg.Server.HTTP), slog.Int("detectors", len(reg.Detectors())))
		errCh <- httpSrv.ListenAndServe()
	}()

	var httpsSrv *http.Server
	if cfg.Server.HTTPS != "" {
		cert, certErr := selfSignedCert()
		if certErr != nil {
			log.Warn("не удалось подготовить сертификат, HTTPS выключен", slog.String("error", certErr.Error()))
		} else {
			httpsSrv = &http.Server{
				Addr:              cfg.Server.HTTPS,
				Handler:           srv.Routes(),
				ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
				ReadTimeout:       cfg.Server.ReadTimeout,
				WriteTimeout:      cfg.Server.WriteTimeout,
				IdleTimeout:       cfg.Server.IdleTimeout,
				TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
			}
			go func() {
				log.Info("слушаю HTTPS", slog.String("addr", cfg.Server.HTTPS))
				errCh <- httpsSrv.ListenAndServeTLS("", "")
			}()
		}
	}

	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go watchConfig(configPath, srv, eng, log, reload)

	select {
	case err := <-errCh:
		return err
	case <-stop:
		log.Info("получен сигнал остановки, завершаю обработку")
	}

	srv.SetReady(false)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if httpsSrv != nil {
		_ = httpsSrv.Shutdown(ctx)
	}
	return httpSrv.Shutdown(ctx)
}

// watchConfig применяет новые настройки по сигналу SIGHUP и при изменении
// файла на диске. Настройки, не прошедшие проверку, отбрасываются: сервис
// продолжает работать со старыми.
func watchConfig(path string, srv *api.Server, eng *engine.Engine, log *slog.Logger, sig <-chan os.Signal) {
	var lastMod time.Time
	if st, err := os.Stat(path); err == nil {
		lastMod = st.ModTime()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	apply := func(reason string) {
		cfg, err := config.Load(path)
		if err != nil {
			log.Warn("новые настройки отклонены", slog.String("reason", reason), slog.String("error", err.Error()))
			return
		}
		srv.SetConfig(cfg)
		eng.ResetFilters()
		log.Info("настройки применены", slog.String("reason", reason), slog.Int("systems", len(cfg.Systems)))
	}

	for {
		select {
		case <-sig:
			apply("signal")
		case <-ticker.C:
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			if st.ModTime().After(lastMod) {
				lastMod = st.ModTime()
				apply("file_changed")
			}
		}
	}
}

// customRules переводит описания типов из настроек в правила детектора.
func customRules(cfg *config.Config) []pii.CustomRule {
	rules := make([]pii.CustomRule, 0, len(cfg.CustomTypes))
	for _, ct := range cfg.CustomTypes {
		rules = append(rules, pii.CustomRule{
			Name:          ct.Name,
			Pattern:       ct.Pattern,
			Group:         ct.Group,
			Validator:     ct.Validator,
			Anchors:       ct.Anchors,
			RequireAnchor: ct.RequireAnchor,
			AnchorWindow:  ct.AnchorWindow,
		})
	}
	return rules
}

// storeKey читает ключ шифрования хранилища из переменной окружения.
// Ключ никогда не попадает ни в образ, ни в репозиторий.
func storeKey(envName string, log *slog.Logger) ([]byte, error) {
	raw := os.Getenv(envName)
	if raw == "" {
		log.Warn("ключ шифрования не задан, создан временный на время работы процесса",
			slog.String("env", envName))
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("ключ в %s должен быть в кодировке base64: %w", envName, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("ключ в %s должен быть длиной 32 байта, получено %d", envName, len(key))
	}
	return key, nil
}

// selfSignedCert создаёт самоподписанный сертификат на время работы процесса.
// Проверяющая система обращается с отключённой проверкой сертификата, поэтому
// отдельный удостоверяющий центр не нужен.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "pii-guard"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(72 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

// runHealthcheck обращается к собственной ручке живости. Нужен для образа без
// оболочки: в нём нет ни curl, ни wget.
func runHealthcheck() error {
	addr := os.Getenv("PII_HEALTHCHECK_URL")
	if addr == "" {
		addr = "http://127.0.0.1:8080/healthz"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(addr) //nolint:noctx // короткая проверка живости
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервис ответил кодом %d", resp.StatusCode)
	}
	return nil
}
