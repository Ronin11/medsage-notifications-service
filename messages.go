package main

import (
	"fmt"
	"strings"
	"time"

	"medsage/notifications-service/store"
)

// Kind names an alert. The values are part of the push payload contract with
// the mobile app (data.kind) and of the notification_sent dedupe key.
type Kind string

const (
	KindMissedDose     Kind = "missed_dose"
	KindDeviceOffline  Kind = "device_offline"
	KindDispenseFailed Kind = "dispense_failed"
	KindDoseTaken      Kind = "dose_taken"
)

// subject is whose dispenser an alert is about, as a reader should see it.
//
// Messages are rendered per reader: the owner is the patient (or the account
// holder for them), so "Mom missed…" becomes "You missed…" on their own phone.
// Names only, never pronouns — we do not know anyone's.
type subject struct {
	person string // the owner's display name; empty when not known
	device string // the device's name; empty when not set
	loc    *time.Location
}

func subjectFor(d store.Device, loc *time.Location) subject {
	return subject{
		person: strings.TrimSpace(d.OwnerDisplayName),
		device: strings.TrimSpace(d.Name),
		loc:    loc,
	}
}

// dispenser is "Your dispenser", "Mom's dispenser", "Kitchen dispenser" or
// "The dispenser", capitalised for the start of a sentence.
func (s subject) dispenser(owner bool) string {
	switch {
	case owner:
		return "Your dispenser"
	case s.person != "":
		return possessive(s.person) + " dispenser"
	case s.device != "":
		return s.device
	default:
		return "The dispenser"
	}
}

// dispenserMid is dispenser() for the middle of a sentence.
func (s subject) dispenserMid(owner bool) string {
	d := s.dispenser(owner)
	if owner || (s.person == "" && s.device == "") {
		return strings.ToLower(d[:1]) + d[1:]
	}
	return d
}

func possessive(name string) string {
	if strings.HasSuffix(name, "s") || strings.HasSuffix(name, "S") {
		return name + "'"
	}
	return name + "'s"
}

// clock formats a wall-clock slot the way people say it: "8:00 AM".
func clock(hour, minute int) string {
	return time.Date(2000, 1, 1, hour, minute, 0, 0, time.UTC).Format("3:04 PM")
}

// since says when something happened relative to now, in the device's zone:
// "6:40 AM" today, "yesterday at 9:15 PM", or "Sep 23 at 9:15 PM".
func since(t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	y1, m1, d1 := lt.Date()
	y2, m2, d2 := ln.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return lt.Format("3:04 PM")
	case lt.AddDate(0, 0, 1).Format("2006-01-02") == ln.Format("2006-01-02"):
		return "yesterday at " + lt.Format("3:04 PM")
	default:
		return lt.Format("Jan 2 at 3:04 PM")
	}
}

// medLabel is "Metformin 500mg", or just the name when no dosage is recorded.
func medLabel(s store.Schedule) string {
	return strings.TrimSpace(strings.TrimSpace(s.MedicationName) + " " + strings.TrimSpace(s.Dosage))
}

// doseNoun is "the 8:00 AM Metformin 500mg dose", "the 8:00 AM Metformin
// 500mg and Lisinopril 10mg doses", or "the 8:00 AM dose" when the event could
// not be tied to a schedule.
func doseNoun(hour, minute int, meds []store.Schedule) string {
	return "the " + clock(hour, minute) + " " + doseTail(meds)
}

func doseTail(meds []store.Schedule) string {
	if len(meds) == 0 {
		return "dose"
	}
	labels := make([]string, len(meds))
	for i, m := range meds {
		labels[i] = medLabel(m)
	}
	if len(labels) == 1 {
		return labels[0] + " dose"
	}
	return strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1] + " doses"
}

// missedText is a dose past its window with nothing recorded, while the
// dispenser was online (or said so itself).
func missedText(s subject, owner bool, hour, minute int, meds []store.Schedule) (string, string) {
	dose := doseNoun(hour, minute, meds)
	switch {
	case owner:
		return "Missed dose", "You missed " + dose
	case s.person != "":
		return "Missed dose", s.person + " missed " + dose
	default:
		// "Kitchen dispenser missed the dose" reads as a fault; say what
		// happened instead.
		return "Missed dose", capitalise(dose) + " on " + s.dispenserMid(false) + " was missed"
	}
}

// unrecordedText is a dose past its window while the dispenser was offline:
// it may have been taken, we cannot know, so say exactly that much.
func unrecordedText(s subject, owner bool, hour, minute int, meds []store.Schedule, lastSeen *time.Time, now time.Time) (string, string) {
	var what string
	switch {
	case owner:
		what = "your " + clock(hour, minute) + " " + doseTailShort(meds)
	case s.person != "":
		what = possessive(s.person) + " " + clock(hour, minute) + " " + doseTailShort(meds)
	default:
		what = doseNoun(hour, minute, meds)
	}
	state := "is offline"
	if lastSeen != nil {
		state = "has been offline since " + since(*lastSeen, now, s.loc)
	}
	// The name is already in the sentence once; "Mom's … — Mom's dispenser"
	// says it twice.
	disp := s.dispenserMid(owner)
	if !owner && s.person != "" {
		disp = "the dispenser"
	}
	return "No record of a dose", "No record of " + what + " — " + disp + " " + state
}

// doseTailShort drops the word "dose" after a named medication, which reads
// naturally after a possessive ("Mom's 8:00 AM Metformin 500mg").
func doseTailShort(meds []store.Schedule) string {
	if len(meds) == 0 {
		return "dose"
	}
	return strings.TrimSuffix(strings.TrimSuffix(doseTail(meds), " doses"), " dose")
}

func offlineText(s subject, owner bool, lastSeen time.Time, now time.Time) (string, string) {
	return "Dispenser offline", s.dispenser(owner) + " has been offline since " + since(lastSeen, now, s.loc) +
		". Doses taken meanwhile won't show until it reconnects — check its power and Wi-Fi."
}

// releaseProblem says what a failed release result means, in words.
func releaseProblem(result string) string {
	switch result {
	case "jammed":
		return "it jammed"
	case "timeout":
		return "it timed out"
	case "busy":
		return "it was busy"
	case "no_hardware":
		return "no release mechanism is fitted"
	default:
		return result
	}
}

func dispenseFailedText(s subject, owner bool, hour, minute int, meds []store.Schedule, compartment uint32, result string) (string, string) {
	return "Dispenser problem", fmt.Sprintf("%s couldn't open compartment %d for %s (%s). The dose is still due.",
		s.dispenser(owner), compartment, doseNoun(hour, minute, meds), releaseProblem(result))
}

// takenText is a dose confirmed taken. afterMissed is set when a missed-dose
// alert for it already went out, so the reader knows this settles that one.
func takenText(s subject, owner bool, hour, minute int, meds []store.Schedule, at time.Time, afterMissed bool) (string, string) {
	title := "Dose taken"
	if afterMissed {
		title = "Dose taken late"
	}
	dose := doseNoun(hour, minute, meds)
	when := " at " + at.In(s.loc).Format("3:04 PM")
	switch {
	case owner:
		return title, "You took " + dose + when
	case s.person != "":
		return title, s.person + " took " + dose + when
	default:
		return title, capitalise(dose) + " on " + s.dispenserMid(false) + " was taken" + when
	}
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
