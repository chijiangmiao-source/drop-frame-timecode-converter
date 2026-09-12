// Package timecode implements drop-frame timecode arithmetic for the
// 30000/1001 and 60000/1001 rates used by broadcast media systems.
//
// Frame indices count real frames elapsed since 00:00:00;00 of the day,
// with the first frame of the day being index 0. Timecode labels follow
// the NTSC drop-frame convention: within every hour, at second 00 of
// every minute whose minute value is not divisible by ten, the first
// 2 (nominal 30 fps) or 4 (nominal 60 fps) frame labels are skipped.
// Skipped labels do not exist and are rejected as illegal.
package timecode

import (
	"fmt"
	"regexp"
	"strconv"
)

// Code is a stable machine-readable error code returned by the API.
type Code string

const (
	CodeInvalidRate           Code = "INVALID_RATE"
	CodeInvalidDirection      Code = "INVALID_DIRECTION"
	CodeMissingField          Code = "MISSING_FIELD"
	CodeInvalidTimecodeFormat Code = "INVALID_TIMECODE_FORMAT"
	CodeDroppedFrameLabel     Code = "DROPPED_FRAME_LABEL"
	CodeFrameIndexOutOfRange  Code = "FRAME_INDEX_OUT_OF_RANGE"
)

// Error describes a single invalid input field.
type Error struct {
	Code    Code
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Rate describes one of the supported drop-frame rates.
type Rate struct {
	id         string
	nominalFPS int
	dropCount  int
}

// Supported rates: NTSC 29.97 (nominal 30) and 59.94 (nominal 60).
var (
	Rate2997 = Rate{id: "30000/1001", nominalFPS: 30, dropCount: 2}
	Rate5994 = Rate{id: "60000/1001", nominalFPS: 60, dropCount: 4}
)

// ID returns the canonical rate string, e.g. "30000/1001".
func (r Rate) ID() string { return r.id }

// NominalFPS returns the nominal frame rate used for numbering (30 or 60).
func (r Rate) NominalFPS() int { return r.nominalFPS }

// DropCount returns how many frame labels are skipped per dropped minute.
func (r Rate) DropCount() int { return r.dropCount }

// ParseRate resolves a rate string. Only "30000/1001" and "60000/1001"
// are accepted.
func ParseRate(s string) (Rate, bool) {
	switch s {
	case Rate2997.id:
		return Rate2997, true
	case Rate5994.id:
		return Rate5994, true
	default:
		return Rate{}, false
	}
}

var timecodePattern = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2});(\d{2})$`)

// ParseTimecode strictly validates a HH:MM:SS;FF label for the given rate:
// HH 00-23, MM 00-59, SS 00-59, FF 00 to nominalFPS-1. It does not reject
// dropped labels; use FrameIndexFromTimecode for full validation.
func ParseTimecode(rate Rate, tc string) (h, m, s, f int, err error) {
	fail := func(format string, args ...any) (int, int, int, int, error) {
		return 0, 0, 0, 0, &Error{
			Code:    CodeInvalidTimecodeFormat,
			Field:   "timecode",
			Message: fmt.Sprintf(format, args...),
		}
	}
	match := timecodePattern.FindStringSubmatch(tc)
	if match == nil {
		return fail("timecode %q must match the strict format HH:MM:SS;FF", tc)
	}
	h, _ = strconv.Atoi(match[1])
	m, _ = strconv.Atoi(match[2])
	s, _ = strconv.Atoi(match[3])
	f, _ = strconv.Atoi(match[4])
	switch {
	case h > 23:
		return fail("timecode %q has hour %02d outside 00-23", tc, h)
	case m > 59:
		return fail("timecode %q has minute %02d outside 00-59", tc, m)
	case s > 59:
		return fail("timecode %q has second %02d outside 00-59", tc, s)
	case f >= rate.nominalFPS:
		return fail("timecode %q has frame %02d outside 00-%02d for rate %s",
			tc, f, rate.nominalFPS-1, rate.id)
	}
	return h, m, s, f, nil
}

// IsDroppedLabel reports whether the label is skipped by the drop-frame
// rule: second 00 of any minute not divisible by ten skips the first
// DropCount frame labels.
func IsDroppedLabel(rate Rate, minute, second, frame int) bool {
	return minute%10 != 0 && second == 0 && frame < rate.dropCount
}

// FrameIndex converts an already validated timecode to the number of real
// frames elapsed since 00:00:00;00 of the same day (first frame is 0).
func FrameIndex(rate Rate, h, m, s, f int) int64 {
	totalMinutes := h*60 + m
	nominal := int64(h*3600+m*60+s)*int64(rate.nominalFPS) + int64(f)
	dropped := int64(rate.dropCount) * int64(totalMinutes-totalMinutes/10)
	return nominal - dropped
}

// FrameIndexFromTimecode validates a timecode label fully (format, ranges
// and drop-frame legality) and returns its frame index.
func FrameIndexFromTimecode(rate Rate, tc string) (int64, error) {
	h, m, s, f, err := ParseTimecode(rate, tc)
	if err != nil {
		return 0, err
	}
	if IsDroppedLabel(rate, m, s, f) {
		return 0, &Error{
			Code:  CodeDroppedFrameLabel,
			Field: "timecode",
			Message: fmt.Sprintf("timecode %q is a label skipped by the drop-frame rule at rate %s and does not exist",
				tc, rate.id),
		}
	}
	return FrameIndex(rate, h, m, s, f), nil
}

// MaxFrameIndex returns the last legal frame index of the day, i.e. the
// index of 23:59:59;(nominalFPS-1).
func MaxFrameIndex(rate Rate) int64 {
	return FrameIndex(rate, 23, 59, 59, rate.nominalFPS-1)
}

// ComponentsFromFrameIndex maps a frame index back to its unique legal
// timecode components. Indices outside [0, MaxFrameIndex] are rejected.
func ComponentsFromFrameIndex(rate Rate, index int64) (h, m, s, f int, err error) {
	maxIndex := MaxFrameIndex(rate)
	if index < 0 || index > maxIndex {
		return 0, 0, 0, 0, &Error{
			Code:  CodeFrameIndexOutOfRange,
			Field: "frame_index",
			Message: fmt.Sprintf("frame_index %d is outside the legal range 0..%d for rate %s",
				index, maxIndex, rate.id),
		}
	}

	fps := int64(rate.nominalFPS)
	drop := int64(rate.dropCount)
	framesPerMinute := fps*60 - drop
	framesPer10Minutes := framesPerMinute*10 + drop

	// Re-insert the skipped labels: 9 dropped minutes per 10-minute block,
	// plus the dropped minutes elapsed inside the current block.
	d := index / framesPer10Minutes
	rem := index % framesPer10Minutes
	nominal := index + drop*9*d
	if rem >= drop {
		nominal += drop * ((rem - drop) / framesPerMinute)
	}

	h = int(nominal / (fps * 3600))
	nominal %= fps * 3600
	m = int(nominal / (fps * 60))
	nominal %= fps * 60
	s = int(nominal / fps)
	f = int(nominal % fps)
	return h, m, s, f, nil
}

// TimecodeFromFrameIndex maps a frame index back to its unique legal
// HH:MM:SS;FF label.
func TimecodeFromFrameIndex(rate Rate, index int64) (string, error) {
	h, m, s, f, err := ComponentsFromFrameIndex(rate, index)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d:%02d:%02d;%02d", h, m, s, f), nil
}
