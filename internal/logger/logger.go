package logger

import (
	"log"
	"os"
)

var (
	// Logger is the default logger instance
	Logger = log.New(os.Stderr, "", log.LstdFlags)
)

// Warnf logs a warning message
func Warnf(format string, v ...interface{}) {
	Logger.Printf("[WARN] "+format, v...)
}

// Infof logs an info message
func Infof(format string, v ...interface{}) {
	Logger.Printf("[INFO] "+format, v...)
}

// Errorf logs an error message
func Errorf(format string, v ...interface{}) {
	Logger.Printf("[ERROR] "+format, v...)
}
