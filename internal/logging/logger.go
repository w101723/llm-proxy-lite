package logging

import (
	"fmt"
	"log"
)

type Logger struct {
	level string
}

func New(level string) *Logger {
	return &Logger{level: level}
}

func (l *Logger) Debug(format string, args ...any) {
	if l.level == "debug" {
		log.Printf("[DEBUG] %s", fmt.Sprintf(format, args...))
	}
}

func (l *Logger) Info(format string, args ...any) {
	if l.level != "none" {
		log.Printf("[INFO] %s", fmt.Sprintf(format, args...))
	}
}

func (l *Logger) Warn(format string, args ...any) {
	if l.level != "none" {
		log.Printf("[WARN] %s", fmt.Sprintf(format, args...))
	}
}

func (l *Logger) Error(format string, args ...any) {
	log.Printf("[ERROR] %s", fmt.Sprintf(format, args...))
}
