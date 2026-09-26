package main

import (
	"testing"
	"time"

	"medsage/notifications-service/store"
)

func TestMedIDMatchesDeviceService(t *testing.T) {
	// fnv1a-32 of the canonical UUID string; the value device-service's
	// medID and the app's medIdFor produce for the same schedule. A known
	// vector from the FNV reference, then the empty string's offset basis.
	if got := medIDFor("a"); got != 0xe40c292c {
		t.Errorf("medIDFor(a) = %#x", got)
	}
	if got := medIDFor(""); got != 0x811c9dc5 {
		t.Errorf("medIDFor('') = %#x", got)
	}
}

func TestParseSlot(t *testing.T) {
	for in, want := range map[string][2]int{"08:00": {8, 0}, "8:05": {8, 5}, "23:59": {23, 59}, " 00:00 ": {0, 0}} {
		h, m, ok := parseSlot(in)
		if !ok || h != want[0] || m != want[1] {
			t.Errorf("parseSlot(%q) = %d,%d,%v", in, h, m, ok)
		}
	}
	for _, in := range []string{"", "8", "24:00", "08:60", "ab:cd", "-1:00"} {
		if _, _, ok := parseSlot(in); ok {
			t.Errorf("parseSlot(%q) accepted", in)
		}
	}
}

func sched(id string, created time.Time, times ...string) store.Schedule {
	return store.Schedule{ID: id, MedicationName: "Med " + id, Dosage: "1mg", Times: times, CreatedAt: created}
}

var longAgo = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func TestDueMissesWindow(t *testing.T) {
	kolkata := mustZone("Asia/Kolkata")
	auckland := mustZone("Pacific/Auckland")
	cases := []struct {
		name    string
		loc     *time.Location
		timeout int
		sched   store.Schedule
		events  []store.DoseEvent
		now     time.Time
		want    []string // occurrences (without schedule id)
	}{
		{"before the window closes", denver, 15, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 9, 26, 8, 14, 59, 0, denver), nil},
		{"exactly at slot+window", denver, 15, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 9, 26, 8, 15, 0, 0, denver), []string{"2026-09-26|08:00"}},
		{"window from the device", denver, 45, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 9, 26, 8, 30, 0, 0, denver), nil},
		{"last moment of the 6h lookback", denver, 15, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 9, 26, 14, 14, 59, 0, denver), []string{"2026-09-26|08:00"}},
		{"past the 6h lookback", denver, 15, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 9, 26, 14, 15, 0, 0, denver), nil},
		{"Never (0) produces nothing", denver, 0, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 9, 26, 9, 0, 0, 0, denver), nil},
		{"late-evening slot, checked after midnight", denver, 15, sched("s1", longAgo, "23:50"), nil,
			time.Date(2026, 9, 27, 0, 30, 0, 0, denver), []string{"2026-09-26|23:50"}},
		{"slot in the device zone, not UTC", kolkata, 15, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 9, 26, 2, 45, 0, 0, time.UTC), []string{"2026-09-26|08:00"}},
		{"zone ahead of UTC across the date line", auckland, 15, sched("s1", longAgo, "07:00"), nil,
			// 07:30 NZST on the 27th is 19:30 UTC on the 26th.
			time.Date(2026, 9, 26, 19, 30, 0, 0, time.UTC), []string{"2026-09-27|07:00"}},
		{"confirmed: not missed", denver, 15, sched("s1", longAgo, "08:00"),
			[]store.DoseEvent{{Type: "medication_confirmed", Hour: 8, CreatedAt: time.Date(2026, 9, 26, 8, 3, 0, 0, denver)}},
			time.Date(2026, 9, 26, 9, 0, 0, 0, denver), nil},
		{"reported missed by the device: not ours to report", denver, 15, sched("s1", longAgo, "08:00"),
			[]store.DoseEvent{{Type: "medication_missed", Hour: 8, MedID: medIDFor("s1"), CreatedAt: time.Date(2026, 9, 26, 8, 15, 0, 0, denver)}},
			time.Date(2026, 9, 26, 9, 0, 0, 0, denver), nil},
		{"yesterday's confirmation does not count", denver, 15, sched("s1", longAgo, "08:00"),
			[]store.DoseEvent{{Type: "medication_confirmed", Hour: 8, CreatedAt: time.Date(2026, 9, 25, 8, 3, 0, 0, denver)}},
			time.Date(2026, 9, 26, 9, 0, 0, 0, denver), []string{"2026-09-26|08:00"}},
		{"another slot's confirmation does not count", denver, 15, sched("s1", longAgo, "08:00"),
			[]store.DoseEvent{{Type: "medication_confirmed", Hour: 8, Minute: 30, CreatedAt: time.Date(2026, 9, 26, 8, 3, 0, 0, denver)}},
			time.Date(2026, 9, 26, 9, 0, 0, 0, denver), []string{"2026-09-26|08:00"}},
		{"another medication's confirmation does not count", denver, 15, sched("s1", longAgo, "08:00"),
			[]store.DoseEvent{{Type: "medication_confirmed", Hour: 8, MedID: medIDFor("s2"), CreatedAt: time.Date(2026, 9, 26, 8, 3, 0, 0, denver)}},
			time.Date(2026, 9, 26, 9, 0, 0, 0, denver), []string{"2026-09-26|08:00"}},
		{"schedule created after the slot", denver, 15, sched("s1", time.Date(2026, 9, 26, 10, 0, 0, 0, denver), "08:00"), nil,
			time.Date(2026, 9, 26, 10, 1, 0, 0, denver), nil},
		// 2026-03-08: Denver jumps 02:00 MST → 03:00 MDT. A 02:30 slot does
		// not exist on the wall clock; it is due as 03:30 MDT.
		{"DST spring-forward gap: not yet", denver, 15, sched("s1", longAgo, "02:30"), nil,
			time.Date(2026, 3, 8, 3, 44, 0, 0, denver), nil},
		{"DST spring-forward gap: due", denver, 15, sched("s1", longAgo, "02:30"), nil,
			time.Date(2026, 3, 8, 3, 45, 0, 0, denver), []string{"2026-03-08|02:30"}},
		// A slot after the jump is at its wall-clock time, one hour less of
		// absolute time from midnight than the day before.
		{"DST spring-forward: 08:00 is 08:00 MDT", denver, 15, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 3, 8, 14, 15, 0, 0, time.UTC), []string{"2026-03-08|08:00"}},
		// 2026-11-01: Denver falls back 02:00 MDT → 01:00 MST.
		{"DST fall-back: 08:00 is 08:00 MST", denver, 15, sched("s1", longAgo, "08:00"), nil,
			time.Date(2026, 11, 1, 15, 15, 0, 0, time.UTC), []string{"2026-11-01|08:00"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dev := store.Device{ID: "d", MissTimeoutMin: tc.timeout}
			got := dueMisses(dev, []store.Schedule{tc.sched}, tc.events, tc.now, tc.loc)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d misses %v, want %v", len(got), got, tc.want)
			}
			for i, r := range got {
				if occ := r.date + "|" + r.hhmm(); occ != tc.want[i] {
					t.Errorf("occurrence %q, want %q", occ, tc.want[i])
				}
				if r.occurrence(tc.sched.ID) != tc.want[i]+"|"+tc.sched.ID {
					t.Errorf("full key %q", r.occurrence(tc.sched.ID))
				}
			}
		})
	}
}

func TestFallBackRepeatedHourIsOneAlert(t *testing.T) {
	// 01:30 happens twice on 2026-11-01 in Denver. Ticking every minute
	// through the night must produce exactly one dose to announce.
	st, p, _, n := scenario()
	st.schedules[devID] = []store.Schedule{sched(schedID, longAgo, "01:30")}
	c := NewChecker(n, time.Minute, 24*time.Hour)
	for now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC); now.Before(time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)); now = now.Add(time.Minute) {
		c.Tick(t.Context(), now)
	}
	if len(p.calls) != 2 { // one alert, two recipients
		t.Errorf("expected one alert, got %d push calls", len(p.calls))
	}
}

func TestCheckerDedupesAcrossTicks(t *testing.T) {
	_, p, _, n := scenario()
	c := NewChecker(n, time.Minute, 30*time.Minute)
	for m := 15; m < 30; m++ {
		c.Tick(t.Context(), time.Date(2026, 9, 26, 8, m, 0, 0, denver))
	}
	if len(p.calls) != 2 {
		t.Fatalf("expected one alert (2 recipients) over 15 ticks, got %d push calls", len(p.calls))
	}
	if b := bodiesByToken(p)["tok-care"].Body; b != "Mom missed the 8:00 AM Metformin 500mg dose" {
		t.Errorf("online miss body = %q", b)
	}
}

func TestCheckerNeverSettingSendsNothing(t *testing.T) {
	st, p, m, n := scenario()
	d := st.devices[devID]
	d.MissTimeoutMin = 0
	st.devices[devID] = d
	NewChecker(n, time.Minute, 30*time.Minute).Tick(t.Context(), time.Date(2026, 9, 26, 12, 0, 0, 0, denver))
	if len(p.calls) != 0 || len(m.sent) != 0 {
		t.Errorf("Never produced alerts: %d pushes, %d emails", len(p.calls), len(m.sent))
	}
}

func TestOfflineAlertOncePerSpell(t *testing.T) {
	st, p, _, n := scenario()
	st.schedules[devID] = []store.Schedule{sched(schedID, longAgo, "20:00")} // nothing due in the morning
	d := st.devices[devID]
	d.Online = false
	seen := time.Date(2026, 9, 26, 6, 40, 0, 0, denver)
	d.LastSeen = &seen
	st.devices[devID] = d
	c := NewChecker(n, time.Minute, 30*time.Minute)

	c.Tick(t.Context(), seen.Add(29*time.Minute))
	if len(p.calls) != 0 {
		t.Fatalf("alerted before OFFLINE_ALERT_MIN: %d", len(p.calls))
	}
	c.Tick(t.Context(), seen.Add(30*time.Minute))
	c.Tick(t.Context(), seen.Add(90*time.Minute))
	if len(p.calls) != 2 {
		t.Fatalf("expected one offline alert, got %d push calls", len(p.calls))
	}
	got := bodiesByToken(p)
	if c := got["tok-care"]; c.Title != "Dispenser offline" || c.Data["kind"] != "device_offline" ||
		c.Body != "Mom's dispenser has been offline since 6:40 AM. Doses taken meanwhile won't show until it reconnects — check its power and Wi-Fi." {
		t.Errorf("caretaker got %+v", c)
	}
	if o := got["tok-owner"]; o.Body[:len("Your dispenser")] != "Your dispenser" {
		t.Errorf("owner got %q", o.Body)
	}

	// Back online, then offline again: a new spell, a new alert.
	later := seen.Add(3 * time.Hour)
	d.LastSeen = &later
	st.devices[devID] = d
	c.Tick(t.Context(), later.Add(31*time.Minute))
	if len(p.calls) != 4 {
		t.Errorf("second spell not alerted: %d push calls", len(p.calls))
	}
}

func TestOfflineAlertRespectsDeviceProblemsPref(t *testing.T) {
	st, p, _, n := scenario()
	st.schedules[devID] = []store.Schedule{sched(schedID, longAgo, "20:00")}
	st.recipients[devID][1].Prefs = store.Prefs{MissedDoses: true}
	d := st.devices[devID]
	d.Online = false
	seen := time.Date(2026, 9, 26, 12, 0, 0, 0, denver)
	d.LastSeen = &seen
	st.devices[devID] = d
	NewChecker(n, time.Minute, 30*time.Minute).Tick(t.Context(), seen.Add(time.Hour))
	if _, ok := bodiesByToken(p)["tok-care"]; ok {
		t.Error("caretaker without device_problems was told")
	}
}

func TestDoseForEvent(t *testing.T) {
	a := sched("a0000000-0000-4000-8000-000000000001", longAgo, "08:00")
	b := sched("b0000000-0000-4000-8000-000000000002", longAgo, "08:00", "20:00")
	c := sched("c0000000-0000-4000-8000-000000000003", longAgo, "12:00")
	all := []store.Schedule{a, b, c}
	cases := []struct {
		name     string
		hour     int
		minute   int
		medID    uint32
		at       time.Time
		wantDate string
		wantIDs  []string
	}{
		{"med_id picks one of two at 8:00", 8, 0, medIDFor(b.ID), time.Date(2026, 9, 26, 8, 15, 0, 0, denver), "2026-09-26", []string{b.ID}},
		{"no med_id: every schedule at the slot", 8, 0, 0, time.Date(2026, 9, 26, 8, 15, 0, 0, denver), "2026-09-26", []string{a.ID, b.ID}},
		{"unknown med_id: unnamed", 8, 0, 12345, time.Date(2026, 9, 26, 8, 15, 0, 0, denver), "2026-09-26", nil},
		{"reported after midnight belongs to yesterday", 23, 50, 0, time.Date(2026, 9, 27, 0, 5, 0, 0, denver), "2026-09-26", nil},
		{"taken early before midnight belongs to tomorrow", 0, 10, 0, time.Date(2026, 9, 26, 23, 50, 0, 0, denver), "2026-09-27", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := doseForEvent(tc.hour, tc.minute, tc.medID, tc.at, all, denver)
			if r.date != tc.wantDate {
				t.Errorf("date %s, want %s", r.date, tc.wantDate)
			}
			if len(r.meds) != len(tc.wantIDs) {
				t.Fatalf("meds %v, want %v", r.meds, tc.wantIDs)
			}
			for i, m := range r.meds {
				if m.ID != tc.wantIDs[i] {
					t.Errorf("med %d = %s, want %s", i, m.ID, tc.wantIDs[i])
				}
			}
		})
	}
}
