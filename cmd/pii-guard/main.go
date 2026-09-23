// Команда pii-guard запускает сервис маскирования персональных данных.
package main

import (
	"bufio"
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
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"pii-guard/internal/api"
	"pii-guard/internal/capture"
	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/logging"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "путь к файлу настроек")
	checkConfig := flag.Bool("check-config", false, "проверить настройки и выйти")
	healthcheck := flag.Bool("healthcheck", false, "проверить живость сервиса и выйти")
	pprofAddr := flag.String("pprof", "", "адрес отдельного слушателя профилировщика, например 127.0.0.1:6060; пусто означает выключено")
	flag.Parse()

	if *healthcheck {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	out := newLogWriter(os.Stdout, logBufferBytes)
	out.flushEvery(logFlushInterval)

	// Журнал запуска собирается по одному окружению: файл настроек ещё не
	// прочитан, а отказ его чтения надо куда-то записать. Аудит в нём
	// выключен намеренно — файл аудита открывается ровно один раз, и делает
	// это окончательный журнал.
	bootCfg := logging.FromEnv()
	bootCfg.Output = out
	bootCfg.Audit.Enabled = false
	boot, err := logging.New(bootCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := boot.Slog()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("настройки не приняты", logging.Event(logging.EventConfigRejected), logging.Err(err))
		stopLog(boot, out)
		os.Exit(1)
	}
	// Предупреждения настроек не мешают работать, но молчать о них нельзя:
	// выключенная система снаружи выглядит как отказ в доступе без причины.
	for _, w := range cfg.Warnings {
		log.Warn("настройки: "+w, logging.Event(logging.EventConfigRejected))
	}

	if *checkConfig {
		fmt.Println("настройки корректны")
		for _, w := range cfg.Warnings {
			fmt.Println("  предупреждение:", w)
		}
		stopLog(boot, out)
		return
	}

	// Окончательный журнал: настройки из файла, поверх них окружение.
	lg, err := logging.New(logConfig(cfg, out))
	if err != nil {
		log.Error("журнал не собран по настройкам", logging.Event(logging.EventConfigRejected), logging.Err(err))
		stopLog(boot, out)
		os.Exit(1)
	}
	_ = boot.Close()
	log = lg.Slog()

	startPprof(*pprofAddr, log)

	err = run(cfg, *configPath, lg)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("сервис остановлен с ошибкой", logging.Event(logging.EventServiceStop), logging.Err(err))
		stopLog(lg, out)
		os.Exit(1)
	}
	stopLog(lg, out)
}

// stopLog закрывает журнал и выталкивает буфер. Нужен отдельной функцией
// потому, что os.Exit пропускает отложенные вызовы: у каждого выхода из
// main закрытие приходится делать явно, а без него последние записи аудита
// и последние счётчики заглушенных повторов до вывода не дойдут.
func stopLog(lg *logging.Logger, out *logWriter) {
	_ = lg.Close()
	out.Flush()
}

// logConfig сливает настройки журнала из файла и из окружения.
//
// Порядок: значения по умолчанию, поверх них файл, поверх них окружение.
// Окружение важнее файла, и это привычное правило для секретов и уровней:
// файл лежит в образе и описывает установку целиком, окружение правится на
// конкретной машине и описывает её особенности — поднятый на время уровень,
// свой путь для журнала аудита, имя копии.
//
// Что именно задано в окружении, определяется сравнением со значением по
// умолчанию: FromEnv возвращает готовый набор поверх умолчаний, поэтому
// отличие поля означает, что переменная задана. Разбирать переменные здесь
// во второй раз нельзя: их имена перечислены в internal/logging, и второй
// список — это второе место, где о новой переменной забудут.
func logConfig(cfg *config.Config, out io.Writer) logging.Config {
	def := logging.DefaultConfig()
	env := logging.FromEnv()
	file := cfg.Logging

	c := def
	// Файл поверх умолчаний. Незаданные поля файла уже заполнены значениями
	// по умолчанию при разборе настроек, поэтому проверять их здесь не надо.
	c.Level = file.Level
	c.Format = file.Format
	c.RepeatWindow = file.RepeatWindow
	c.RepeatMinLevel = file.RepeatLevel
	c.SampleN = file.SampleN
	c.Slow = file.Slow
	if file.Source != nil {
		c.AddSource = *file.Source
	}
	if file.Redact != nil {
		c.Redact = *file.Redact
	}
	if file.Audit.Enabled != nil {
		c.Audit.Enabled = *file.Audit.Enabled
	}
	c.Audit.Path = file.Audit.Path
	if file.Audit.MaxBytes > 0 {
		c.Audit.MaxBytes = file.Audit.MaxBytes
	}
	if file.Audit.Keep > 0 {
		c.Audit.Keep = file.Audit.Keep
	}

	// Окружение поверх файла.
	if env.Level != def.Level {
		c.Level = env.Level
	}
	if env.Format != def.Format {
		c.Format = env.Format
	}
	if env.AddSource != def.AddSource {
		c.AddSource = env.AddSource
	}
	if env.Redact != def.Redact {
		c.Redact = env.Redact
	}
	if env.RepeatWindow != def.RepeatWindow {
		c.RepeatWindow = env.RepeatWindow
	}
	if env.RepeatMinLevel != def.RepeatMinLevel {
		c.RepeatMinLevel = env.RepeatMinLevel
	}
	if env.SampleN != def.SampleN {
		c.SampleN = env.SampleN
	}
	if env.Slow != def.Slow {
		c.Slow = env.Slow
	}
	if env.Audit.Enabled != def.Audit.Enabled {
		c.Audit.Enabled = env.Audit.Enabled
	}
	if env.Audit.Path != "" {
		c.Audit.Path = env.Audit.Path
	}
	if env.Audit.MaxBytes > 0 {
		c.Audit.MaxBytes = env.Audit.MaxBytes
	}
	if env.Audit.Keep > 0 {
		c.Audit.Keep = env.Audit.Keep
	}
	// Имя копии и версия сборки приходят только из окружения: в файле,
	// общем для всех копий, им места нет.
	c.Instance = env.Instance
	c.Version = env.Version

	// Приёмник журнала буферизован. Без буфера каждая запись — отдельный
	// системный вызов, и на штатной нагрузке он стоит дороже самого разбора
	// текста: замер дал сорок процентов процессора сервиса на одном выводе
	// журнала против 0.94 процента после буферизации.
	c.Output = out
	return c
}

// Параметры буфера журнала. Размер буфера выбран так, чтобы в него помещалось
// около двухсот записей обычной длины, а шаг сброса — так, чтобы записи
// появлялись в хранилище журнала практически сразу.
const (
	logBufferBytes   = 64 << 10
	logFlushInterval = 200 * time.Millisecond
)

// logWriter — буферизованный приёмник журнала. Без буфера каждая запись это
// отдельный системный вызов записи, и на штатной нагрузке он стоит дороже
// самого разбора текста: замер профилировщиком дал сорок процентов процессора
// сервиса на одном только выводе журнала.
//
// Записи не теряются и не переставляются: буфер сбрасывается по заполнению,
// с постоянным шагом и при завершении работы. В худшем случае при жёстком
// падении процесса теряется то, что накопилось за шаг сброса.
type logWriter struct {
	mu  sync.Mutex
	buf *bufio.Writer
}

func newLogWriter(w io.Writer, size int) *logWriter {
	return &logWriter{buf: bufio.NewWriterSize(w, size)}
}

// Write принимает готовую запись журнала. Замок здесь обязателен: свой замок
// журнала защищает только сборку записи, а не приёмник.
func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// Flush выталкивает накопленное.
func (w *logWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.buf.Flush()
}

// flushEvery выталкивает буфер с постоянным шагом, чтобы редкие записи не
// залёживались в памяти до заполнения буфера.
func (w *logWriter) flushEvery(d time.Duration) {
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for range t.C {
			w.Flush()
		}
	}()
}

func run(cfg *config.Config, configPath string, lg *logging.Logger) error {
	log := lg.Slog()
	key, err := storeKey(cfg.Store.KeyEnv, log)
	if err != nil {
		return err
	}
	st, err := store.New(store.Config{
		Key:        key,
		TTL:        cfg.Store.TTL,
		MaxRecords: cfg.Store.MaxRecords,
		Redis:      cfg.Store.Redis,
	})
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
		pii.NewExtraDetector(),
	)
	if err := applyCustomTypes(reg, cfg); err != nil {
		return fmt.Errorf("правила custom_types: %w", err)
	}

	m := metrics.New()
	// Показатели самого журнала в общем реестре: по ним видно, срабатывала ли
	// защита от утечки и сколько записей съели глушитель повторов и
	// прореживание. Без регистрации они остались бы внутри процесса.
	if rerr := m.Register(logging.NewCollector(lg)); rerr != nil {
		log.Warn("показатели журнала не зарегистрированы",
			logging.Component("metrics"), logging.Err(rerr))
	}
	// Сбой отдельного детектора пропускает его тип, но не роняет ответ.
	reg.OnPanic(func(types []pii.Type, recovered any) {
		name := "unknown"
		if len(types) > 0 {
			name = string(types[0])
		}
		m.ObservePanic(name)
		log.Error("сбой детектора",
			logging.Event(logging.EventDetectorPanic),
			logging.Component("engine"),
			slog.String("type", name), slog.Any("panic", recovered))
	})
	eng := engine.New(reg)
	// Сбой обработки куска длинного текста пропускает кусок, но не роняет
	// процесс. Без этой привязки перехват работал бы молча.
	eng.OnPanic(func(chunk int, recovered any) {
		m.ObservePanic("chunk")
		log.Error("сбой обработки куска",
			logging.Event(logging.EventDetectorPanic),
			logging.Component("engine"),
			slog.Int("chunk", chunk), slog.Any("panic", recovered))
	})
	srv := api.New(cfg, st, eng, m, log)
	srv.SetLogging(lg)

	// Канал сохранения запросов. Выключенный канал файла не открывает, и
	// проверять признак здесь не нужно. Ошибка открытия файла сервис не
	// останавливает: захват — вспомогательная задача, ради неё отказывать в
	// обслуживании нельзя, но промолчать о ней тоже нельзя.
	capw, err := capture.New(capture.Config{
		Enabled:     cfg.Capture.Enabled,
		Path:        cfg.Capture.Path,
		MaxBytes:    cfg.Capture.MaxBytes,
		Keep:        cfg.Capture.Keep,
		WithPayload: cfg.Capture.WithPayload,
		Queue:       cfg.Capture.Queue,
	}, logging.NewRotatingWriter)
	if err != nil {
		log.Error("захват запросов не включился",
			logging.Event("capture_failed"), logging.Component("capture"),
			slog.String("path", cfg.Capture.Path), slog.Any("error", err))
	} else {
		srv.SetCapture(capw)
		defer func() { _ = capw.Close() }()
		if capw.Enabled() {
			log.Info("захват запросов включён",
				logging.Event("capture_on"), logging.Component("capture"),
				slog.String("path", cfg.Capture.Path),
				slog.Bool("with_payload", capw.WithPayload()))
		}
	}
	// Ручка управления правилами правит тот же файл, из которого сервис
	// прочитал настройки, и её правка подхватывается обычным перечитыванием.
	srv.SetConfigPath(configPath)
	watchStoreDegradation(cfg, st, srv, m, log)

	// Маршруты собираются один раз и отдаются обоим слушателям. Второй вызов
	// Routes дал бы вторую прослойку журнала со своим счётчиком прореживания,
	// и коэффициент на деле оказался бы вдвое мягче заданного.
	handler := srv.Routes()
	httpSrv := &http.Server{
		Addr:              cfg.Server.HTTP,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	log.Info("сервис запущен",
		logging.Event(logging.EventServiceStart),
		slog.String("log_level", lg.LevelName()),
		slog.Int("sample_n", cfg.Logging.SampleN),
		slog.Bool("shared_store", st.Shared()),
		slog.Bool("audit", lg.Audit().Enabled()))

	errCh := make(chan error, 2)
	go func() {
		log.Info("слушаю HTTP",
			logging.Event(logging.EventListen), logging.Component("api"),
			slog.String("addr", cfg.Server.HTTP), slog.Int("detectors", len(reg.Detectors())))
		errCh <- httpSrv.ListenAndServe()
	}()

	var httpsSrv *http.Server
	if cfg.Server.HTTPS != "" {
		cert, certErr := selfSignedCert()
		if certErr != nil {
			log.Warn("не удалось подготовить сертификат, HTTPS выключен",
				logging.Component("api"), logging.Err(certErr))
		} else {
			httpsSrv = &http.Server{
				Addr:              cfg.Server.HTTPS,
				Handler:           handler,
				ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
				ReadTimeout:       cfg.Server.ReadTimeout,
				WriteTimeout:      cfg.Server.WriteTimeout,
				IdleTimeout:       cfg.Server.IdleTimeout,
				TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
			}
			go func() {
				log.Info("слушаю HTTPS",
					logging.Event(logging.EventListen), logging.Component("api"),
					slog.String("addr", cfg.Server.HTTPS))
				errCh <- httpsSrv.ListenAndServeTLS("", "")
			}()
		}
	}

	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	applyConfig := newConfigApplier(configPath, srv, eng, reg, log)
	// Ручка управления правилами применяет свою правку тем же кодом, что и
	// наблюдение за файлом, только сразу, а не к ближайшему обходу.
	srv.SetReloader(func() error { return applyConfig("rules_api") })
	go watchConfig(configPath, applyConfig, log, reload)

	select {
	case err := <-errCh:
		return err
	case <-stop:
		log.Info("получен сигнал остановки, снимаю готовность",
			logging.Event(logging.EventServiceStop), logging.Component("api"),
			slog.String("drain", cfg.Server.DrainTimeout.String()))
	}

	srv.SetReady(false)
	drain(cfg.Server.DrainTimeout, stop, log)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if httpsSrv != nil {
		_ = httpsSrv.Shutdown(ctx)
	}
	return httpSrv.Shutdown(ctx)
}

// drain держит приём открытым после снятия готовности.
//
// Балансировщик узнаёт о снятии готовности не сразу: при периоде проверки в
// две секунды и пороге в две неудачи на это уходит до пяти секунд, и ещё две
// заложены запасом на разброс проверок. Всё это время он продолжает слать
// запросы на копию. Закрыть слушатель в ту же миллисекунду, в которую снята
// готовность, значит отказать по каждому такому запросу: при контрактной
// тысяче запросов в секунду это оценочно до 2 500 потерянных запросов на
// каждую остановку, то есть 0.8 процента пятиминутного прогона.
//
// Пауза прерывается вторым сигналом остановки: администратор, нажавший
// остановку дважды, хочет немедленно, и спорить с ним неправильно. Подробный
// расчёт — в docs/BALANCING.md, раздел о мягком выводе копии из обслуживания.
func drain(d time.Duration, stop <-chan os.Signal, log *slog.Logger) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		log.Info("окно вывода из обслуживания выдержано, закрываю приём",
			logging.Event(logging.EventServiceStop), logging.Component("api"),
			logging.Took(d))
	case <-stop:
		log.Warn("второй сигнал остановки, закрываю приём немедленно",
			logging.Event(logging.EventServiceStop), logging.Component("api"))
	}
}

// watchStoreDegradation связывает деградацию общего хранилища с показателями,
// журналом и готовностью копии.
//
// Деградация обязана быть видна в показателях: молчаливый переход на память
// процесса означает, что копии сервиса перестали видеть записи друг друга.
//
// Снимать ли при этом готовность, решает настройка. Разбор того, почему по
// умолчанию выбрано щадящее поведение, — в комментарии к полю
// config.Store.UnreadyOnDegraded. Коротко: на одной копии память процесса это
// полноценный запасной путь и работать лучше, чем не работать, а в группе
// копий ответы становятся тихо неверными, и тогда копии место вне
// обслуживания.
//
// Обратно готовность сама не возвращается. Хранилище сообщает об отказах, но
// не о восстановлении, а возвращать копию в строй по догадке хуже, чем
// оставить решение человеку: копия жива, отвечает на проверку живости и
// пересоздана не будет.
func watchStoreDegradation(cfg *config.Config, st *store.Store, srv *api.Server, m *metrics.Metrics, log *slog.Logger) {
	var withdrawn atomic.Bool
	st.SetDegradedHook(func(op string, degErr error) {
		m.ObserveDegraded("store")
		if !cfg.Store.UnreadyOnDegraded {
			if degErr != nil {
				log.Warn("общее хранилище недоступно, работаем через память процесса",
					logging.Event(logging.EventStoreDegraded), logging.Component("store"),
					slog.String(logging.FieldOp, op), logging.Err(degErr))
			}
			return
		}
		if withdrawn.Swap(true) {
			return
		}
		srv.SetReady(false)
		log.Error("общее хранилище недоступно, копия уходит из обслуживания",
			logging.Event(logging.EventStoreDegraded), logging.Component("store"),
			slog.String(logging.FieldOp, op), logging.Err(degErr))
	})
}

// startPprof поднимает отдельный слушатель встроенного профилировщика Go.
// Профилировщик живёт на своём адресе, а не на рабочем порту: его ручки отдают
// дамп памяти и состояние горутин, и посторонним их видеть нельзя. По умолчанию
// он выключен, включается ключом и адресом на петлевом интерфейсе, куда ходят
// через туннель.
func startPprof(addr string, log *slog.Logger) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// Снятие профиля длится десятки секунд, поэтому срок записи ответа
		// здесь заведомо длиннее рабочего.
		WriteTimeout: 5 * time.Minute,
	}
	go func() {
		log.Info("слушаю профилировщик",
			logging.Event(logging.EventListen), logging.Component("pprof"),
			slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("слушатель профилировщика остановлен",
				logging.Component("pprof"), logging.Err(err))
		}
	}()
}

// watchConfig применяет новые настройки по сигналу SIGHUP и при изменении
// файла на диске. Настройки, не прошедшие проверку, отбрасываются: сервис
// продолжает работать со старыми.
func watchConfig(path string, apply func(reason string) error, log *slog.Logger, sig <-chan os.Signal) {
	var lastMod time.Time
	if st, err := os.Stat(path); err == nil {
		lastMod = st.ModTime()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Возвращает признак применения: отклонённые настройки не должны
	// двигать отметку времени, иначе следующая правка потеряется.
	applied := func(reason string) bool {
		err := apply(reason)
		if err != nil {
			log.Warn("новые настройки отклонены",
				logging.Event(logging.EventConfigRejected), logging.Component("config"),
				slog.String("reason", reason), logging.Err(err))
			return false
		}
		return true
	}

	for {
		select {
		case <-sig:
			_ = applied("signal")
		case <-ticker.C:
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			// Отметка времени двигается ТОЛЬКО при удачном применении.
			// Прежний порядок двигал её и при отказе, поэтому исправление,
			// внесённое в ту же секунду, не подхватывалось: точность времени
			// изменения на многих файловых системах равна секунде. Оператор
			// правил опечатку, сервис молчал, и выглядело это так, будто
			// настройки вообще не перечитываются.
			//
			// Побочное следствие: сломанный файл перечитывается каждые пять
			// секунд, пока его не починят. Журнал от этого не пухнет, потому
			// что одинаковые записи глушатся повторами.
			if st.ModTime().After(lastMod) {
				if applied("file_changed") {
					lastMod = st.ModTime()
				}
			}
		}
	}
}

// newConfigApplier собирает функцию применения настроек из файла.
//
// Одна и та же функция используется наблюдением за файлом и ручкой управления
// правилами: две отдельные реализации разошлись бы, и правка через интерфейс
// начала бы применяться иначе, чем правка файла руками.
//
// Настройки применяются целиком или никак. Полупримененные настройки — это
// системы, которым роздан список типов, искать которые нечем.
func newConfigApplier(path string, srv *api.Server, eng *engine.Engine, reg *pii.Registry, log *slog.Logger) func(reason string) error {
	return func(reason string) error {
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		// Детектор своих типов пересобирается до подмены настроек: если новое
		// правило не компилируется, настройки не применяются вовсе.
		if err := applyCustomTypes(reg, cfg); err != nil {
			return err
		}
		srv.SetConfig(cfg)
		eng.ResetFilters()
		log.Info("настройки применены",
			logging.Event(logging.EventConfigApplied), logging.Component("config"),
			slog.String("reason", reason), slog.Int("systems", len(cfg.Systems)),
			slog.Int("custom_types", len(cfg.CustomTypes)))
		return nil
	}
}

// applyCustomTypes пересобирает детектор типов из настроек.
//
// Вызывается и при запуске, и при каждом применении новых настроек. До
// появления сменного слота детектор собирался только при запуске, и
// добавленный в настройки тип молча не действовал до перезапуска сервиса:
// журнал писал «настройки применены», а искать новый тип было нечем.
//
// Неверное правило оставляет прежний детектор нетронутым. Это важнее, чем
// применить настройки наполовину: иначе одна опечатка в новом типе
// обесточила бы все остальные.
func applyCustomTypes(reg *pii.Registry, cfg *config.Config) error {
	if len(cfg.CustomTypes) == 0 {
		reg.SetCustom(nil)
		return nil
	}
	custom, err := pii.NewCustomDetector(customRules(cfg))
	if err != nil {
		return err
	}
	reg.SetCustom(custom)
	return nil
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
			logging.Component("store"), slog.String("env", envName))
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
