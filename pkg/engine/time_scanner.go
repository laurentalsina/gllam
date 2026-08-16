package engine

import (
	"fmt"
	"time"
)

// timeScanner is an sql.Scanner that reads a TEXT column stored as a timestamp
// string back into a time.Time. go-sqlite3 returns TEXT columns as string,
// which database/sql cannot automatically convert to *time.Time.
//
// go-sqlite3 writes time.Time values using a space separator
// ("2006-01-02 15:04:05.999999999-07:00"), not the T-separator of RFC3339,
// so we try both families of formats.
type timeScanner struct {
	t *time.Time
}

func scanTime(t *time.Time) timeScanner { return timeScanner{t} }

// sqliteFormats lists the formats go-sqlite3 uses when storing time.Time as TEXT.
// Ordered from most to least precise so the first successful parse wins.
var sqliteFormats = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

func (ts timeScanner) Scan(value any) error {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case time.Time:
		*ts.t = v
		return nil
	case string:
		for _, layout := range sqliteFormats {
			if parsed, err := time.Parse(layout, v); err == nil {
				*ts.t = parsed
				return nil
			}
		}
		return fmt.Errorf("cannot parse %q as a time value", v)
	default:
		return fmt.Errorf("unsupported type for time.Time scan: %T", value)
	}
}
