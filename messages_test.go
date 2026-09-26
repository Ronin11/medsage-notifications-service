package main

import (
	"strings"
	"testing"
	"time"

	"medsage/notifications-service/store"
)

func TestMessageText(t *testing.T) {
	met := store.Schedule{MedicationName: "Metformin", Dosage: "500mg"}
	lis := store.Schedule{MedicationName: "Lisinopril", Dosage: "10mg"}
	mom := subject{person: "Mom", device: "Kitchen dispenser", loc: denver}
	named := subject{device: "Kitchen dispenser", loc: denver}
	bare := subject{loc: denver}
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, denver)
	seenToday := time.Date(2026, 9, 26, 6, 40, 0, 0, denver)
	seenYesterday := time.Date(2026, 9, 25, 21, 15, 0, 0, denver)
	seenLastWeek := time.Date(2026, 9, 20, 21, 15, 0, 0, denver)

	two := func(a, b string) string { return a + " / " + b }
	cases := []struct{ name, got, want string }{
		{"missed, caretaker", two(missedText(mom, false, 8, 0, []store.Schedule{met})),
			"Missed dose / Mom missed the 8:00 AM Metformin 500mg dose"},
		{"missed, owner", two(missedText(mom, true, 8, 0, []store.Schedule{met})),
			"Missed dose / You missed the 8:00 AM Metformin 500mg dose"},
		{"missed, no schedule", two(missedText(mom, false, 20, 30, nil)),
			"Missed dose / Mom missed the 8:30 PM dose"},
		{"missed, two medications", two(missedText(mom, false, 8, 0, []store.Schedule{met, lis})),
			"Missed dose / Mom missed the 8:00 AM Metformin 500mg and Lisinopril 10mg doses"},
		{"missed, no owner name", two(missedText(named, false, 8, 0, []store.Schedule{met})),
			"Missed dose / The 8:00 AM Metformin 500mg dose on Kitchen dispenser was missed"},
		{"missed, nothing known", two(missedText(bare, false, 8, 0, nil)),
			"Missed dose / The 8:00 AM dose on the dispenser was missed"},
		{"unrecorded, caretaker", two(unrecordedText(mom, false, 8, 0, []store.Schedule{met}, &seenToday, now)),
			"No record of a dose / No record of Mom's 8:00 AM Metformin 500mg — the dispenser has been offline since 6:40 AM"},
		{"unrecorded, owner", two(unrecordedText(mom, true, 8, 0, []store.Schedule{met}, &seenYesterday, now)),
			"No record of a dose / No record of your 8:00 AM Metformin 500mg — your dispenser has been offline since yesterday at 9:15 PM"},
		{"unrecorded, device name", two(unrecordedText(named, false, 8, 0, []store.Schedule{met}, &seenLastWeek, now)),
			"No record of a dose / No record of the 8:00 AM Metformin 500mg dose — Kitchen dispenser has been offline since Sep 20 at 9:15 PM"},
		{"unrecorded, no last_seen", two(unrecordedText(mom, false, 8, 0, nil, nil, now)),
			"No record of a dose / No record of Mom's 8:00 AM dose — the dispenser is offline"},
		{"offline, caretaker", two(offlineText(mom, false, seenToday, now)),
			"Dispenser offline / Mom's dispenser has been offline since 6:40 AM. Doses taken meanwhile won't show until it reconnects — check its power and Wi-Fi."},
		{"offline, device name", two(offlineText(named, false, seenToday, now)),
			"Dispenser offline / Kitchen dispenser has been offline since 6:40 AM. Doses taken meanwhile won't show until it reconnects — check its power and Wi-Fi."},
		{"offline, name ending in s", two(offlineText(subject{person: "James", loc: denver}, false, seenToday, now)),
			"Dispenser offline / James' dispenser has been offline since 6:40 AM. Doses taken meanwhile won't show until it reconnects — check its power and Wi-Fi."},
		{"dispense failed", two(dispenseFailedText(mom, false, 8, 0, []store.Schedule{met}, 3, "jammed")),
			"Dispenser problem / Mom's dispenser couldn't open compartment 3 for the 8:00 AM Metformin 500mg dose (it jammed). The dose is still due."},
		{"taken", two(takenText(mom, false, 8, 0, []store.Schedule{met}, time.Date(2026, 9, 26, 8, 12, 0, 0, denver), false)),
			"Dose taken / Mom took the 8:00 AM Metformin 500mg dose at 8:12 AM"},
		{"taken, owner, late", two(takenText(mom, true, 8, 0, []store.Schedule{met}, time.Date(2026, 9, 26, 9, 30, 0, 0, denver), true)),
			"Dose taken late / You took the 8:00 AM Metformin 500mg dose at 9:30 AM"},
		{"midnight and noon", clock(0, 5) + " " + clock(12, 0), "12:05 AM 12:00 PM"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s:\n got  %q\n want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestMessagesNeverCarryAnID(t *testing.T) {
	// The old text was "Device a1b2c3: patient missed…". An id means nothing
	// to a caretaker and is not something a lock screen should show.
	s := subjectFor(store.Device{ID: devID, OwnerDisplayName: "Mom"}, denver)
	_, body := missedText(s, false, 8, 0, nil)
	if strings.Contains(body, devID[:6]) || strings.Contains(strings.ToLower(body), "device") {
		t.Errorf("body mentions the device id: %q", body)
	}
}
