package auth

import (
	"sync"
	"time"
)

// Ограничение неудачных входов: не больше limit неудач за окно span на пару
// «логин + адрес». Пара, а не один логин: иначе посторонний мог бы запереть
// чужую учётную запись, перебирая её пароль со своего адреса. Пороги приходят
// из настроек (Policy) на каждый вызов — их меняют без перезапуска.

// limiter считает неудачные входы в памяти процесса.
//
// Не в базе и не в Redis: экземпляр API один, а после перезапуска счётчики
// обнуляются — это пять лишних попыток, а не уязвимость.
type limiter struct {
	mu   sync.Mutex
	hits map[string]window
}

type window struct {
	start    time.Time
	failures int
}

func newLimiter() *limiter {
	return &limiter{hits: map[string]window{}}
}

// blocked отвечает, заперт ли ключ, и на сколько ещё.
func (l *limiter) blocked(key string, now time.Time, limit int, span time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.hits[key]
	if !ok {
		return false, 0
	}

	end := w.start.Add(span)
	if !now.Before(end) {
		delete(l.hits, key)
		return false, 0
	}

	if w.failures >= limit {
		return true, end.Sub(now)
	}

	return false, 0
}

func (l *limiter) fail(key string, now time.Time, span time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Заодно выметаются истёкшие окна: иначе карта росла бы на каждом
	// новом адресе до перезапуска.
	for k, w := range l.hits {
		if !now.Before(w.start.Add(span)) {
			delete(l.hits, k)
		}
	}

	w, ok := l.hits[key]
	if !ok {
		w = window{start: now}
	}

	w.failures++
	l.hits[key] = w
}

// reset вызывается после удачного входа: прошлые опечатки не должны
// копиться до следующей.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.hits, key)
}
