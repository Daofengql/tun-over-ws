package logger

import (
	"os"
	"strings"

	"github.com/rs/zerolog"
)

var (
	// Logger is the global zerolog logger with colored console output.
	Logger zerolog.Logger
)

func init() {
	Setup("info")
}

// Setup initializes the global logger with the given level.
func Setup(level string) {
	lvl, err := zerolog.ParseLevel(strings.ToLower(level))
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	output := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		FormatLevel: func(i interface{}) string {
			var l string
			if ll, ok := i.(string); ok {
				switch ll {
				case "trace":
					l = colorize("TRC", 90)
				case "debug":
					l = colorize("DBG", 36)
				case "info":
					l = colorize("INF", 32)
				case "warn":
					l = colorize("WRN", 33)
				case "error":
					l = colorize("ERR", 31)
				case "fatal":
					l = colorize("FTL", 31)
				default:
					l = ll
				}
			}
			return l
		},
		FormatMessage: func(i interface{}) string {
			if msg, ok := i.(string); ok {
				return colorize(msg, 0)
			}
			return ""
		},
		FormatFieldName: func(i interface{}) string {
			if name, ok := i.(string); ok {
				return colorize(name+"=", 36)
			}
			return ""
		},
		FormatFieldValue: func(i interface{}) string {
			if val, ok := i.(string); ok {
				return colorize(val, 35)
			}
			return ""
		},
	}

	Logger = zerolog.New(output).
		Level(lvl).
		With().
		Timestamp().
		Logger()
}

// colorize wraps text with ANSI color codes.
func colorize(text string, color int) string {
	if color == 0 {
		return text
	}
	return "\033[" + itoa(color) + "m" + text + "\033[0m"
}

func itoa(i int) string {
	if i >= 100 {
		return string([]byte{byte('0' + i/100), byte('0' + i/10%10), byte('0' + i%10)})
	}
	if i >= 10 {
		return string([]byte{byte('0' + i/10), byte('0' + i%10)})
	}
	return string([]byte{byte('0' + i)})
}
