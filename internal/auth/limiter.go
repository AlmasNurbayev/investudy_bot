package auth

import (
	"sync"
	"time"
)

// Ограничение неудачных входов: не больше maxFailures за failureWindow на
// пару «логин + адрес». Пара, а не один логин: иначе посторонний мог бы
// запереть чужую учётную запись, перебирая её пароль со своего адреса.
const (
	maxFailures   = 5
	failureWindow = 15 * time.Minute
)

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
func (l *limiter) blocked(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.hits[key]
	if !ok {
		return false, 0
	}

	end := w.start.Add(failureWindow)
	if !now.Before(end) {
		delete(l.hits, key)
		return false, 0
	}

	if w.failures >= maxFailures {
		return true, end.Sub(now)
	}

	return false, 0
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Заодно выметаются истёкшие окна: иначе карта росла бы на каждом
	// новом адресе до перезапуска.
	for k, w := range l.hits {
		if !now.Before(w.start.Add(failureWindow)) {
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
