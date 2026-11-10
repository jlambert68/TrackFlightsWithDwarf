package applog

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	infoLogger  = log.New(os.Stderr, "", log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC)
	errorLogger = log.New(os.Stderr, "", log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC)
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	listenerMu  sync.RWMutex
	listeners   = make(map[uint64]func(string))
	nextID      uint64
)

// Subscribe receives each application log entry after it has been flattened to
// one line. The returned function removes the listener.
func Subscribe(listener func(string)) func() {
	if listener == nil {
		return func() {}
	}

	listenerMu.Lock()
	nextID++
	id := nextID
	listeners[id] = listener
	listenerMu.Unlock()

	return func() {
		listenerMu.Lock()
		delete(listeners, id)
		listenerMu.Unlock()
	}
}

func InfofID(id, format string, args ...any) {
	output(infoLogger, "INFO", id, format, args...)
}

func ErrorfID(id, format string, args ...any) {
	output(errorLogger, "ERROR", id, format, args...)
}

func DebugfID(id, format string, args ...any) {
	output(infoLogger, "DEBUG", id, format, args...)
}

func output(logger *log.Logger, level, id, format string, args ...any) {
	if !uuidPattern.MatchString(strings.ToLower(strings.TrimSpace(id))) {
		panic(fmt.Sprintf("invalid log UUID: %q", id))
	}
	message := singleLine(fmt.Sprintf(format, args...))
	entry := fmt.Sprintf("level=%s ID=%s %s", level, id, message)
	_ = logger.Output(3, entry)
	notifyListeners(time.Now().UTC().Format("2006-01-02 15:04:05.000000 UTC ") + entry)
}

func singleLine(message string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(message))
}

func notifyListeners(entry string) {
	listenerMu.RLock()
	snapshot := make([]func(string), 0, len(listeners))
	for _, listener := range listeners {
		snapshot = append(snapshot, listener)
	}
	listenerMu.RUnlock()

	for _, listener := range snapshot {
		listener(entry)
	}
}
