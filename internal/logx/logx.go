// Package logx 带内存环形缓冲的日志（面板可读），同时输出到 stdout。
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Level string

const (
	Info  Level = "info"
	Warn  Level = "warn"
	Error Level = "error"
)

type Entry struct {
	Time  int64  `json:"time"`
	Level Level  `json:"level"`
	Tag   string `json:"tag"`
	Msg   string `json:"msg"`
}

const ringSize = 600

var (
	mu      sync.Mutex
	ring    = make([]Entry, 0, ringSize)
	chans   []chan Entry
	logDir  string
	logFile *os.File
	logDay  string
)

// InitFile 开启日志落盘（data/logs/）。
func InitFile(dir string) {
	mu.Lock()
	logDir = dir
	mu.Unlock()
}

func writeLogFile(line string) {
	if logDir == "" {
		return
	}
	day := time.Now().Format("2006-01-02")
	if logFile == nil || logDay != day {
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
		}
		_ = os.MkdirAll(logDir, 0o700)
		f, err := os.OpenFile(filepath.Join(logDir, "tokenhub-"+day+".log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		logFile = f
		logDay = day
	}
	_, _ = logFile.WriteString(line + "\n")
}

func log(level Level, tag, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	e := Entry{Time: time.Now().Unix(), Level: level, Tag: tag, Msg: msg}
	mu.Lock()
	if len(ring) >= ringSize {
		ring = ring[1:]
	}
	ring = append(ring, e)
	cs := append([]chan Entry(nil), chans...)
	line := time.Unix(e.Time, 0).Format("2006-01-02 15:04:05") + " [" + strings.ToUpper(string(level)) + "] " + tag + ": " + msg
	writeLogFile(line)
	mu.Unlock()
	for _, c := range cs {
		select {
		case c <- e:
		default:
		}
	}
	if level == Error {
		fmt.Fprintln(os.Stderr, line)
	} else {
		fmt.Fprintln(os.Stdout, line)
	}
}

// Subscribe 返回一个实时日志通道（面板 SSE 用）。
func Subscribe() chan Entry {
	mu.Lock()
	defer mu.Unlock()
	ch := make(chan Entry, 64)
	chans = append(chans, ch)
	return ch
}

func Unsubscribe(ch chan Entry) {
	mu.Lock()
	defer mu.Unlock()
	for i, c := range chans {
		if c == ch {
			chans = append(chans[:i], chans[i+1:]...)
			break
		}
	}
}

func Infof(tag, format string, args ...any)  { log(Info, tag, format, args...) }
func Warnf(tag, format string, args ...any)  { log(Warn, tag, format, args...) }
func Errorf(tag, format string, args ...any) { log(Error, tag, format, args...) }

// Last 返回最近 n 条（时间升序）。
func Last(n int) []Entry {
	mu.Lock()
	defer mu.Unlock()
	out := append([]Entry(nil), ring...)
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}
