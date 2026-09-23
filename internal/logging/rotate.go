package logging

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// rotatingWriter — файл с ротацией по размеру. Внешних зависимостей у сервиса
// нет, а журнал аудита пишется в файл и обязан быть ограничен по месту,
// поэтому ротация сделана своими силами.
//
// Схема простая и предсказуемая: при превышении размера текущий файл
// переименовывается в файл с номером 1, номер 1 становится номером 2 и так
// далее, самый старый удаляется. Сжатия нет намеренно: сжимать файл в том же
// процессе, который обслуживает запросы, значит отдать ему ядро в самый
// неподходящий момент.
type rotatingWriter struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func newRotatingWriter(path string, max int64, keep int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	w := &rotatingWriter{path: path, max: max, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size+int64(len(p)) > w.max && w.size > 0 {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	for i := w.keep; i >= 1; i-- {
		src := w.path + "." + strconv.Itoa(i)
		if i == w.keep {
			_ = os.Remove(src)
			continue
		}
		dst := w.path + "." + strconv.Itoa(i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

// Close закрывает файл.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// NewRotatingWriter открывает файл с ротацией по размеру для использования
// вне пакета. Нужен каналу захвата запросов: он пишет свой файл, живёт по
// своим правилам хранения, но ротация у файлов одна и та же, и второй её
// копии в коде быть не должно.
//
// Файл создаётся с правами только для владельца: в нём могут оказаться
// персональные данные.
func NewRotatingWriter(path string, max int64, keep int) (io.WriteCloser, error) {
	return newRotatingWriter(path, max, keep)
}
