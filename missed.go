package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"medsage/notifications-service/store"
)

// missLookback bounds how late a missed dose is still announced. Past it the
// alert is history, not news — and without it, a service that was down for a
// day would come back and announce a day's worth of doses at once.
const missLookback = 6 * time.Hour

// medIDFor is the med_id a dispenser uses for a schedule: fnv1a-32 of the
// schedule UUID's canonical string. Must match device-service's medID and the
// app's medIdFor, or events stop matching their doses.
func medIDFor(scheduleID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(scheduleID))
	return h.Sum32()
}

// parseSlot reads "HH:MM". ok is false for anything else, which skips that
// slot rather than failing the device.
func parseSlot(s string) (hour, minute int, ok bool) {
	hh, mm, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// doseRef identifies one scheduled dose on one day: the unit a dose alert is
// deduplicated on, whichever path (the dispenser or the periodic check)
// notices it.
type doseRef struct {
	date   string // YYYY-MM-DD in the device's zone
	hour   int
	minute int
	at     time.Time        // the slot as an instant
	meds   []store.Schedule // the schedule(s) it belongs to; empty when unknown
}

func (r doseRef) hhmm() string { return fmt.Sprintf("%02d:%02d", r.hour, r.minute) }

// occurrence is the notification_sent key for one schedule's dose, or for the
// slot as a whole when the dose could not be tied to a schedule.
func (r doseRef) occurrence(scheduleID string) string {
	return r.date + "|" + r.hhmm() + "|" + scheduleID
}

// slotAt is the instant a wall-clock slot falls on a given local date.
//
// A slot inside a spring-forward gap (02:30 on the day clocks jump from 02:00
// to 03:00) does not exist. time.Date resolves it an hour early (01:30), which
// would count the dose missed before the dispenser could have rung for it, so
// it is moved to the equivalent instant after the jump (03:30). A slot inside
// the repeated fall-back hour resolves to the first of its two instants.
// Either way it is one instant per slot per day, which is what the dedupe key
// needs.
func slotAt(y int, m time.Month, d, hour, minute int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, hour, minute, 0, 0, loc)
	if t.Hour() != hour {
		t = t.Add(time.Hour)
	}
	return t
}

// dueMisses returns the doses whose miss window has passed with nothing
// recorded — no confirmation, no miss report, no reconciliation.
//
// A dose is due for this check from slot+window until slot+window+6h. Both
// today's and yesterday's slots are considered because that span crosses
// midnight for a late-evening dose. Events are matched to a slot the way the
// app matches them (doseSchedule.ts computeTodayDoses): same hour and minute,
// recorded on or after the start of the slot's local day, and the same med_id
// unless the event carries none.
func dueMisses(dev store.Device, schedules []store.Schedule, events []store.DoseEvent, now time.Time, loc *time.Location) []doseRef {
	if dev.MissTimeoutMin <= 0 {
		// "Never": the dispenser does not count misses, and a caretaker who
		// chose that does not want the cloud counting them either.
		return nil
	}
	window := time.Duration(dev.MissTimeoutMin) * time.Minute
	localNow := now.In(loc)

	var out []doseRef
	for _, dayOffset := range []int{-1, 0} {
		y, m, d := localNow.AddDate(0, 0, dayOffset).Date()
		dayStart := time.Date(y, m, d, 0, 0, 0, 0, loc)
		for _, sc := range schedules {
			medID := medIDFor(sc.ID)
			for _, t := range sc.Times {
				hour, minute, ok := parseSlot(t)
				if !ok {
					continue
				}
				at := slotAt(y, m, d, hour, minute, loc)
				// A dose that was already past when the schedule was created
				// was never due: adding an 8:00 medication at 10:00 must not
				// report the 8:00 dose missed.
				if at.Before(sc.CreatedAt) {
					continue
				}
				if now.Before(at.Add(window)) || !now.Before(at.Add(window+missLookback)) {
					continue
				}
				if recorded(events, dayStart, hour, minute, medID) {
					continue
				}
				out = append(out, doseRef{
					date: fmt.Sprintf("%04d-%02d-%02d", y, m, d), hour: hour, minute: minute,
					at: at, meds: []store.Schedule{sc},
				})
			}
		}
	}
	return out
}

func recorded(events []store.DoseEvent, dayStart time.Time, hour, minute int, medID uint32) bool {
	for _, e := range events {
		if e.CreatedAt.Before(dayStart) || e.Hour != hour || e.Minute != minute {
			continue
		}
		if e.MedID == 0 || e.MedID == medID {
			return true
		}
	}
	return false
}

// doseForEvent ties a dispenser's event (slot hour:minute, optional med_id,
// when it happened) to a doseRef.
//
// The slot's date is the one that puts the slot nearest the event: a dose due
// at 23:50 and reported missed at 00:05 belongs to yesterday, one taken early
// at 23:50 for a 00:10 slot belongs to tomorrow.
//
// With a med_id the schedule is exact. Without one (older firmware) every
// schedule with that slot is the dose — the dispenser rang once for all of
// them — so they are announced together.
func doseForEvent(hour, minute int, medID uint32, at time.Time, schedules []store.Schedule, loc *time.Location) doseRef {
	local := at.In(loc)
	best := doseRef{hour: hour, minute: minute}
	var bestGap time.Duration = -1
	for _, off := range []int{-1, 0, 1} {
		y, m, d := local.AddDate(0, 0, off).Date()
		slot := slotAt(y, m, d, hour, minute, loc)
		gap := at.Sub(slot)
		if gap < 0 {
			gap = -gap
		}
		if bestGap < 0 || gap < bestGap {
			bestGap = gap
			best.date = fmt.Sprintf("%04d-%02d-%02d", y, m, d)
			best.at = slot
		}
	}

	for _, sc := range schedules {
		if medID != 0 {
			if medIDFor(sc.ID) == medID {
				best.meds = []store.Schedule{sc}
				break
			}
			continue
		}
		for _, t := range sc.Times {
			if h, mi, ok := parseSlot(t); ok && h == hour && mi == minute {
				best.meds = append(best.meds, sc)
				break
			}
		}
	}
	return best
}

// Checker is the server-side missed-dose and offline check.
//
// The dispenser reports a missed dose itself, but only while it is powered and
// online. When it is unplugged or off the network it says nothing, which is
// exactly when a caretaker most needs to hear about it; this check notices the
// silence.
type Checker struct {
	n            *EventNotifier
	interval     time.Duration
	offlineAfter time.Duration
}

func NewChecker(n *EventNotifier, interval, offlineAfter time.Duration) *Checker {
	return &Checker{n: n, interval: interval, offlineAfter: offlineAfter}
}

// Run ticks until ctx is cancelled. A tick that is still running when the
// next is due delays it rather than overlapping it.
func (c *Checker) Run(ctx context.Context) {
	if c.n.store == nil {
		slog.Warn("Missed-dose check disabled: no database")
		return
	}
	slog.Info("Missed-dose check started", "interval", c.interval.String(), "offline_after", c.offlineAfter.String())
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Missed-dose check stopped")
			return
		case <-t.C:
			c.Tick(ctx, c.n.now())
		}
	}
}

// Tick runs one pass over every device with active schedules.
func (c *Checker) Tick(ctx context.Context, now time.Time) {
	devices, err := c.n.store.DevicesWithActiveSchedules(ctx)
	if err != nil {
		slog.Error("Missed-dose check: listing devices failed", "error", err)
		return
	}
	for _, dev := range devices {
		if ctx.Err() != nil {
			return
		}
		c.checkDevice(ctx, dev, now)
	}
}

func (c *Checker) checkDevice(ctx context.Context, dev store.Device, now time.Time) {
	loc := c.n.zone(dev)

	if !dev.Online && dev.LastSeen != nil && now.Sub(*dev.LastSeen) >= c.offlineAfter {
		c.n.announceOffline(ctx, dev, loc, now)
	}

	if dev.MissTimeoutMin <= 0 {
		return
	}
	schedules, err := c.n.store.ActiveSchedules(ctx, dev.ID)
	if err != nil {
		slog.Error("Missed-dose check: schedules failed", "device_id", dev.ID, "error", err)
		return
	}
	y, m, d := now.In(loc).AddDate(0, 0, -1).Date()
	events, err := c.n.store.DoseEvents(ctx, dev.ID, time.Date(y, m, d, 0, 0, 0, 0, loc))
	if err != nil {
		slog.Error("Missed-dose check: events failed", "device_id", dev.ID, "error", err)
		return
	}
	for _, ref := range dueMisses(dev, schedules, events, now, loc) {
		// Debug: until the lookback passes, every tick finds the same dose
		// and the claim turns it away.
		slog.Debug("Missed-dose check: no record of dose",
			"device_id", dev.ID, "occurrence", ref.occurrence(ref.meds[0].ID), "online", dev.Online)
		c.n.announceMissed(ctx, dev, loc, ref, !dev.Online, now)
	}
}
