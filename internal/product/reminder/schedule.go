// Package reminder implements deterministic, timezone-aware reminder schedules.
package reminder

import (
	"errors"
	"sort"
	"time"
)

var ErrInvalid = errors.New("reminder schedule is invalid")

type Schedule struct {
	Timezone  string `json:"timezone"`
	LocalTime string `json:"local_time"`
	Weekdays  []int  `json:"weekdays"`
	Channel   string `json:"channel"`
}

func (schedule Schedule) Validate() error {
	if schedule.Timezone == "" || len(schedule.Timezone) > 128 || schedule.Channel != "email" && schedule.Channel != "push" && schedule.Channel != "in_app" || len(schedule.Weekdays) < 1 || len(schedule.Weekdays) > 7 {
		return ErrInvalid
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return ErrInvalid
	}
	if _, _, err := parseClock(schedule.LocalTime); err != nil {
		return ErrInvalid
	}
	seen := map[int]bool{}
	for _, day := range schedule.Weekdays {
		if day < 1 || day > 7 || seen[day] {
			return ErrInvalid
		}
		seen[day] = true
	}
	return nil
}

func (schedule Schedule) Normalized() Schedule {
	result := schedule
	result.Weekdays = append([]int(nil), schedule.Weekdays...)
	sort.Ints(result.Weekdays)
	return result
}

// Next returns the first instant after now. A nonexistent wall time resolves to
// the first valid local minute after the DST gap. A duplicated wall time uses
// the earliest occurrence still in the future.
func (schedule Schedule) Next(now time.Time) (time.Time, error) {
	if schedule.Validate() != nil || now.IsZero() {
		return time.Time{}, ErrInvalid
	}
	loc, _ := time.LoadLocation(schedule.Timezone)
	hour, minute, _ := parseClock(schedule.LocalTime)
	allowed := map[int]bool{}
	for _, day := range schedule.Weekdays {
		allowed[day] = true
	}
	localNow := now.In(loc)
	for offset := 0; offset < 15; offset++ {
		date := localNow.AddDate(0, 0, offset)
		iso := int(date.Weekday())
		if iso == 0 {
			iso = 7
		}
		if !allowed[iso] {
			continue
		}
		base := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).Add(-18 * time.Hour)
		end := base.Add(72 * time.Hour)
		var fallback time.Time
		exactExists := false
		for instant := base; instant.Before(end); instant = instant.Add(time.Minute) {
			wall := instant.In(loc)
			if wall.Year() != date.Year() || wall.Month() != date.Month() || wall.Day() != date.Day() {
				continue
			}
			if !instant.After(now) {
				continue
			}
			wallMinute := wall.Hour()*60 + wall.Minute()
			requested := hour*60 + minute
			if wallMinute == requested {
				return instant.UTC(), nil
			}
			if wallMinute > requested && (fallback.IsZero() || instant.Before(fallback)) {
				fallback = instant
			}
		}
		for instant := base; instant.Before(end); instant = instant.Add(time.Minute) {
			wall := instant.In(loc)
			if wall.Year() == date.Year() && wall.Month() == date.Month() && wall.Day() == date.Day() && wall.Hour() == hour && wall.Minute() == minute {
				exactExists = true
				break
			}
		}
		if !exactExists && !fallback.IsZero() {
			return fallback.UTC(), nil
		}
	}
	return time.Time{}, ErrInvalid
}

func parseClock(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return 0, 0, ErrInvalid
	}
	return parsed.Hour(), parsed.Minute(), nil
}
