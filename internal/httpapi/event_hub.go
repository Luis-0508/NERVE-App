package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// eventHub owns one PostgreSQL LISTEN connection and fans notifications out to
// all connected SSE clients. The listener is intentionally independent of the
// request pool so browser streams neither consume pool capacity nor block
// pool.Close during graceful shutdown.
type eventHub struct {
	db     *pgxpool.Pool
	logger *slog.Logger

	startOnce sync.Once
	mu        sync.RWMutex
	nextID    uint64
	clients   map[uint64]chan string
}

func newEventHub(db *pgxpool.Pool, logger *slog.Logger) *eventHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &eventHub{db: db, logger: logger, clients: make(map[uint64]chan string)}
}

func (h *eventHub) subscribe() (<-chan string, func()) {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	channel := make(chan string, 32)
	h.clients[id] = channel
	h.mu.Unlock()

	h.startOnce.Do(func() { go h.run() })

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.clients, id)
			h.mu.Unlock()
		})
	}
	return channel, unsubscribe
}

func (h *eventHub) run() {
	if h.db == nil {
		return
	}

	backoff := time.Second
	for {
		config := h.db.Config().ConnConfig.Copy()
		connection, err := pgx.ConnectConfig(context.Background(), config)
		if err != nil {
			h.logger.Warn("event hub database connection failed", "error", err)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}

		if _, err := connection.Exec(context.Background(), `LISTEN nerve_events`); err != nil {
			_ = connection.Close(context.Background())
			h.logger.Warn("event hub LISTEN failed", "error", err)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}

		backoff = time.Second
		for {
			notification, err := connection.WaitForNotification(context.Background())
			if err != nil {
				h.logger.Warn("event hub listener disconnected", "error", err)
				break
			}
			h.broadcast(notification.Payload)
		}
		_ = connection.Close(context.Background())
		time.Sleep(backoff)
	}
}

func (h *eventHub) broadcast(payload string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, channel := range h.clients {
		select {
		case channel <- payload:
		default:
			// A slow browser must not block the single database listener or grow
			// memory without bounds. Event history remains queryable via /events.
		}
	}
}
