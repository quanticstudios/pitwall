package model

import (
	"errors"
	"time"
)

// ParseTaskSchedule resolves an optional local HH:MM start and positive Go
// duration. Without at, a repeat first runs one interval from now.
func ParseTaskSchedule(at, every string, now time.Time) (next time.Time, repeat time.Duration, err error) {
	if every != "" {
		repeat, err = time.ParseDuration(every)
		if err != nil || repeat <= 0 {
			return next, 0, errors.New("repeat must be a positive duration, such as 24h")
		}
		next = now.Add(repeat)
	}
	if at != "" {
		clock, e := time.Parse("15:04", at)
		if e != nil {
			return time.Time{}, 0, errors.New("start at must be HH:MM, such as 02:00")
		}
		next = time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
	}
	return next, repeat, nil
}
