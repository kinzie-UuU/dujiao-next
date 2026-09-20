package gormstore

import (
	"testing"
	"time"
)

func TestSQLiteTimeRangeHandlesStoredTimeFormats(t *testing.T) {
	_, db := setupDashboardRepositoryTest(t)
	if err := db.Exec("CREATE TABLE time_range_probe (created_at TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"2026-09-20 10:00:00+00:00", "2026-09-20T10:00:00Z", "2026-09-20 09:59:59+00:00", "2026-09-20 11:00:00+00:00"} {
		if err := db.Exec("INSERT INTO time_range_probe VALUES (?)", value).Error; err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	where, args := timeRangeQuery(db, "created_at", start, start.Add(time.Hour))
	var count int64
	if err := db.Table("time_range_probe").Where(where, args...).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("want inclusive start/exclusive end across stored formats, got %d", count)
	}
}
