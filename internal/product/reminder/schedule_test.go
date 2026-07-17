package reminder

import (
	"testing"
	"time"
)

func TestNextHandlesDSTGapAndDuplicate(t *testing.T) {
	schedule := Schedule{Timezone: "America/New_York", LocalTime: "02:30", Weekdays: []int{7}, Channel: "push"}
	next, err := schedule.Next(time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC))
	if err != nil || next.In(mustLocation(t, "America/New_York")).Format("2006-01-02 15:04") != "2026-03-08 03:00" {
		t.Fatalf("gap next=%s err=%v", next, err)
	}
	duplicate := Schedule{Timezone: "America/New_York", LocalTime: "01:30", Weekdays: []int{7}, Channel: "email"}
	first, err := duplicate.Next(time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC))
	if err != nil || !first.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) {
		t.Fatalf("first=%s err=%v", first, err)
	}
	second, err := duplicate.Next(time.Date(2026, 11, 1, 5, 45, 0, 0, time.UTC))
	if err != nil || !second.Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("second=%s err=%v", second, err)
	}
}
func TestNextFixedTwoHundredTimezoneScenarios(t *testing.T) {
	zones := []string{"UTC", "Asia/Shanghai", "Europe/London", "America/New_York", "America/Los_Angeles", "Australia/Sydney", "Asia/Tokyo", "Europe/Berlin"}
	for i := 0; i < 200; i++ {
		zone := zones[i%len(zones)]
		schedule := Schedule{Timezone: zone, LocalTime: "09:15", Weekdays: []int{1, 2, 3, 4, 5, 6, 7}, Channel: "in_app"}
		now := time.Date(2026, time.January, 1+i%365, i%23, i%59, 0, 0, time.UTC)
		next, err := schedule.Next(now)
		if err != nil || !next.After(now) {
			t.Fatalf("scenario=%d zone=%s next=%s err=%v", i, zone, next, err)
		}
	}
}
func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	value, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
