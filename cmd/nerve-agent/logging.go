package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const (
	logFileName    = "agent.log"
	logFileMaxSize = 10 << 20
	logFilesKept   = 3
)

// newLogger writes structured JSON logs. The level can be raised or lowered
// after the configuration is loaded.
func newLogger(output io.Writer, level *slog.LevelVar) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
}

// rotatingFile is a size-bounded log file for platforms where a service has no
// log collector (Windows). It keeps agent.log plus agent.log.1 … .N.
type rotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	file    *os.File
	size    int64
}

func openRotatingFile(dir string) (*rotatingFile, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	r := &rotatingFile{path: filepath.Join(dir, logFileName), maxSize: logFileMaxSize, keep: logFilesKept}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	r.file, r.size = file, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, os.ErrClosed
	}
	if r.size+int64(len(p)) > r.maxSize && r.size > 0 {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	r.file = nil
	// Renames replace existing targets, so the oldest file drops off.
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		return err
	}
	return r.open()
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
