package timecode

import (
	"testing"
)

func TestParseRate(t *testing.T) {
	for _, id := range []string{"30000/1001", "60000/1001"} {
		if _, ok := ParseRate(id); !ok {
			t.Errorf("ParseRate(%q) = not ok, want ok", id)
		}
	}
	for _, id := range []string{"", "25", "29.97", "30000/1000", "60000/1000", "30000/1001 "} {
		if _, ok := ParseRate(id); ok {
			t.Errorf("ParseRate(%q) = ok, want not ok", id)
		}
	}
}

func TestFrameIndexBoundaryVectors(t *testing.T) {
	cases := []struct {
		rate Rate
		tc   string
		want int64
	}{
		{Rate2997, "00:00:00;00", 0},
		{Rate2997, "00:00:00;01", 1},
		{Rate2997, "00:00:59;29", 1799},
		{Rate2997, "00:01:00;02", 1800},
		{Rate2997, "00:01:00;29", 1827},
		{Rate2997, "00:09:59;29", 17981},
		{Rate2997, "00:10:00;00", 17982},
		{Rate2997, "00:10:00;01", 17983},
		{Rate2997, "00:11:00;02", 19782},
		{Rate2997, "01:00:00;02", 107894},
		{Rate2997, "23:59:59;29", 2589407},
		{Rate5994, "00:00:00;00", 0},
		{Rate5994, "00:00:59;59", 3599},
		{Rate5994, "00:01:00;04", 3600},
		{Rate5994, "00:09:59;59", 35963},
		{Rate5994, "00:10:00;00", 35964},
		{Rate5994, "00:10:00;01", 35965},
		{Rate5994, "01:00:00;04", 215788},
		{Rate5994, "23:59:59;59", 5178815},
	}
	for _, c := range cases {
		got, err := FrameIndexFromTimecode(c.rate, c.tc)
		if err != nil {
			t.Errorf("FrameIndexFromTimecode(%s, %q) error: %v", c.rate.ID(), c.tc, err)
			continue
		}
		if got != c.want {
			t.Errorf("FrameIndexFromTimecode(%s, %q) = %d, want %d", c.rate.ID(), c.tc, got, c.want)
		}
	}
}

func TestDroppedLabelsAreIllegal(t *testing.T) {
	rates := []Rate{Rate2997, Rate5994}
	for _, rate := range rates {
		for minute := 0; minute < 24*60; minute++ {
			h, m := minute/60, minute%60
			for f := 0; f < rate.DropCount(); f++ {
				tc := label(h, m, 0, f)
				_, err := FrameIndexFromTimecode(rate, tc)
				if m%10 == 0 {
					if err != nil {
						t.Errorf("rate=%s %s: ten-minute boundary label must be legal, got %v", rate.ID(), tc, err)
					}
				} else {
					te, ok := err.(*Error)
					if !ok || te.Code != CodeDroppedFrameLabel || te.Field != "timecode" {
						t.Errorf("rate=%s %s: want DROPPED_FRAME_LABEL on timecode, got %v", rate.ID(), tc, err)
					}
				}
			}
		}
	}
}

func TestInvalidTimecodeFormat(t *testing.T) {
	cases := []struct {
		rate Rate
		tc   string
	}{
		{Rate2997, ""},
		{Rate2997, "0:00:00;00"},
		{Rate2997, "00:00:00:00"},  // wrong separator
		{Rate2997, "00:00:00;0"},   // short frame
		{Rate2997, "00:00:00;000"}, // long frame
		{Rate2997, "24:00:00;00"},  // hour out of range
		{Rate2997, "00:60:00;00"},  // minute out of range
		{Rate2997, "00:00:60;00"},  // second out of range
		{Rate2997, "00:00:00;30"},  // frame out of range for nominal 30
		{Rate2997, "23:59:59;30"},  // frame out of range at end of day
		{Rate5994, "00:00:00;60"},  // frame out of range for nominal 60
		{Rate2997, "aa:bb:cc;dd"},  // non-numeric
		{Rate2997, " 00:00:00;00"}, // leading space
		{Rate2997, "00:00:00;00 "}, // trailing space
	}
	for _, c := range cases {
		_, err := FrameIndexFromTimecode(c.rate, c.tc)
		te, ok := err.(*Error)
		if !ok || te.Code != CodeInvalidTimecodeFormat || te.Field != "timecode" {
			t.Errorf("FrameIndexFromTimecode(%s, %q): want INVALID_TIMECODE_FORMAT, got %v",
				c.rate.ID(), c.tc, err)
		}
	}
}

func TestFrameIndexRange(t *testing.T) {
	rates := []Rate{Rate2997, Rate5994}
	for _, rate := range rates {
		max := MaxFrameIndex(rate)
		if _, err := TimecodeFromFrameIndex(rate, max); err != nil {
			t.Errorf("rate=%s max index %d must be legal, got %v", rate.ID(), max, err)
		}
		for _, idx := range []int64{-1, -100, max + 1, max + 1000} {
			_, err := TimecodeFromFrameIndex(rate, idx)
			te, ok := err.(*Error)
			if !ok || te.Code != CodeFrameIndexOutOfRange || te.Field != "frame_index" {
				t.Errorf("rate=%s index %d: want FRAME_INDEX_OUT_OF_RANGE, got %v", rate.ID(), idx, err)
			}
		}
	}
}

func TestMaxFrameIndexValues(t *testing.T) {
	if got := MaxFrameIndex(Rate2997); got != 2589407 {
		t.Errorf("MaxFrameIndex(Rate2997) = %d, want 2589407", got)
	}
	if got := MaxFrameIndex(Rate5994); got != 5178815 {
		t.Errorf("MaxFrameIndex(Rate5994) = %d, want 5178815", got)
	}
}

func TestSpanFrames(t *testing.T) {
	cases := []struct {
		name    string
		rate    Rate
		start   string
		end     string
		nextDay bool
		want    int64
	}{
		{"30 same frame is zero", Rate2997, "00:10:00;00", "00:10:00;00", false, 0},
		{"30 same day across ten-minute boundary", Rate2997, "00:09:59;29", "00:10:00;01", false, 2},
		{"30 same day plain difference", Rate2997, "00:01:00;02", "01:00:00;02", false, 106094},
		{"60 same frame is zero", Rate5994, "00:10:00;00", "00:10:00;00", false, 0},
		{"60 same day across ten-minute boundary", Rate5994, "00:09:59;59", "00:10:00;03", false, 4},
		{"30 midnight rollover last frame to first", Rate2997, "23:59:59;29", "00:00:00;00", true, 1},
		{"30 midnight rollover into next morning", Rate2997, "23:59:00;02", "00:01:00;02", true, 3598},
		{"60 midnight rollover last frame to second frame", Rate5994, "23:59:59;59", "00:00:00;01", true, 2},
		{"60 midnight rollover into next morning", Rate5994, "23:59:00;04", "00:01:00;04", true, 7196},
	}
	for _, c := range cases {
		got, err := SpanFrames(c.rate, c.start, c.end, c.nextDay)
		if err != nil {
			t.Errorf("%s: SpanFrames error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: SpanFrames = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSpanFramesEndBeforeStart(t *testing.T) {
	for _, rate := range []Rate{Rate2997, Rate5994} {
		_, err := SpanFrames(rate, "00:10:00;01", "00:10:00;00", false)
		te, ok := err.(*Error)
		if !ok || te.Code != CodeEndBeforeStart || te.Field != "end_timecode" {
			t.Errorf("rate=%s: want END_BEFORE_START on end_timecode, got %v", rate.ID(), err)
		}
	}
}

func TestOffsetTimecode(t *testing.T) {
	cases := []struct {
		name    string
		rate    Rate
		tc      string
		offset  int64
		wantTC  string
		wantDay int
	}{
		{"30 zero offset returns same label", Rate2997, "00:10:00;00", 0, "00:10:00;00", 0},
		{"30 zero offset at start of day", Rate2997, "00:00:00;00", 0, "00:00:00;00", 0},
		{"30 zero offset at end of day", Rate2997, "23:59:59;29", 0, "23:59:59;29", 0},
		{"30 in-point moved back across ten-minute boundary", Rate2997, "00:10:00;00", -1, "00:09:59;29", 0},
		{"30 ten minutes back", Rate2997, "00:10:00;00", -17982, "00:00:00;00", 0},
		{"30 forward across ten-minute boundary", Rate2997, "00:09:59;29", 2, "00:10:00;01", 0},
		{"30 last frame steps to next day first frame", Rate2997, "23:59:59;29", 1, "00:00:00;00", 1},
		{"30 first frame steps back to previous day last frame", Rate2997, "00:00:00;00", -1, "23:59:59;29", -1},
		{"30 next-day reach at the far boundary (last+N)", Rate2997, "23:59:59;29", 2589408, "23:59:59;29", 1},
		{"30 previous-day reach at the far boundary (0-N)", Rate2997, "00:00:00;00", -2589408, "00:00:00;00", -1},
		{"60 zero offset returns same label", Rate5994, "01:00:00;04", 0, "01:00:00;04", 0},
		{"60 zero offset at start of day", Rate5994, "00:00:00;00", 0, "00:00:00;00", 0},
		{"60 in-point moved back across ten-minute boundary", Rate5994, "00:10:00;00", -1, "00:09:59;59", 0},
		{"60 ten minutes back", Rate5994, "00:10:00;00", -35964, "00:00:00;00", 0},
		{"60 forward across ten-minute boundary", Rate5994, "00:09:59;59", 2, "00:10:00;01", 0},
		{"60 last frame steps to next day first frame", Rate5994, "23:59:59;59", 1, "00:00:00;00", 1},
		{"60 first frame steps back to previous day last frame", Rate5994, "00:00:00;00", -1, "23:59:59;59", -1},
		{"60 next-day reach at the far boundary", Rate5994, "23:59:59;59", 5178816, "23:59:59;59", 1},
		{"60 previous-day reach at the far boundary", Rate5994, "00:00:00;00", -5178816, "00:00:00;00", -1},
	}
	for _, c := range cases {
		got, err := OffsetTimecode(c.rate, c.tc, c.offset)
		if err != nil {
			t.Errorf("%s: OffsetTimecode error: %v", c.name, err)
			continue
		}
		if got.Timecode != c.wantTC || got.DayOffset != c.wantDay {
			t.Errorf("%s: OffsetTimecode(%s, %d) = %+v, want {%s day %d}",
				c.name, c.tc, c.offset, got, c.wantTC, c.wantDay)
		}
	}
}

func TestOffsetTimecodeReusesLabelValidation(t *testing.T) {
	cases := []struct {
		name      string
		tc        string
		wantCode  Code
		wantField string
	}{
		{"bad format", "00:10:00:00", CodeInvalidTimecodeFormat, "timecode"},
		{"dropped label", "00:01:00;01", CodeDroppedFrameLabel, "timecode"},
		{"hour out of range", "24:00:00;00", CodeInvalidTimecodeFormat, "timecode"},
	}
	for _, c := range cases {
		_, err := OffsetTimecode(Rate2997, c.tc, 10)
		te, ok := err.(*Error)
		if !ok || te.Code != c.wantCode || te.Field != c.wantField {
			t.Errorf("%s: want %s on timecode, got %v", c.name, c.wantCode, err)
		}
	}
}

func TestOffsetOutOfRange(t *testing.T) {
	for _, rate := range []Rate{Rate2997, Rate5994} {
		framesPerDay := MaxFrameIndex(rate) + 1
		cases := []struct {
			name   string
			tc     string
			offset int64
		}{
			{"forward past next day", "23:59:59;" + lastFrameDigits(rate), framesPerDay + 1},
			{"backward past previous day", "00:00:00;00", -framesPerDay - 1},
			{"huge forward offset", "00:10:00;00", framesPerDay*2 + 1},
			{"huge backward offset", "00:10:00;00", -(framesPerDay*2 + 1)},
			{"int64 max offset", "00:10:00;00", 1<<63 - 1},
			{"int64 min offset", "00:10:00;00", -1 << 63},
		}
		for _, c := range cases {
			got, err := OffsetTimecode(rate, c.tc, c.offset)
			te, ok := err.(*Error)
			if !ok || te.Code != CodeOffsetOutOfRange || te.Field != "frame_offset" {
				t.Errorf("%s rate=%s: want OFFSET_OUT_OF_RANGE on frame_offset, got %+v err=%v",
					c.name, rate.ID(), got, err)
			}
			if got.Timecode != "" || got.DayOffset != 0 {
				t.Errorf("%s rate=%s: out-of-range result must not carry a target, got %+v",
					c.name, rate.ID(), got)
			}
		}
	}
}

func lastFrameDigits(rate Rate) string {
	if rate == Rate2997 {
		return "29"
	}
	return "59"
}

func TestSpanFramesFieldErrors(t *testing.T) {
	cases := []struct {
		name      string
		start     string
		end       string
		wantCode  Code
		wantField string
	}{
		{"bad start format", "0:00:00;00", "00:10:00;00", CodeInvalidTimecodeFormat, "start_timecode"},
		{"bad end format", "00:10:00;00", "00:10:00:00", CodeInvalidTimecodeFormat, "end_timecode"},
		{"dropped start label", "00:01:00;00", "00:10:00;00", CodeDroppedFrameLabel, "start_timecode"},
		{"dropped end label", "00:10:00;00", "00:11:00;01", CodeDroppedFrameLabel, "end_timecode"},
	}
	for _, c := range cases {
		_, err := SpanFrames(Rate2997, c.start, c.end, false)
		te, ok := err.(*Error)
		if !ok || te.Code != c.wantCode || te.Field != c.wantField {
			t.Errorf("%s: want %s on %s, got %v", c.name, c.wantCode, c.wantField, err)
		}
	}
}

// TestRoundTripExhaustive proves reversibility over every legal frame index
// of the day for both rates, and that every produced label is legal.
func TestRoundTripExhaustive(t *testing.T) {
	for _, rate := range []Rate{Rate2997, Rate5994} {
		max := MaxFrameIndex(rate)
		prev := ""
		for i := int64(0); i <= max; i++ {
			h, m, s, f, err := ComponentsFromFrameIndex(rate, i)
			if err != nil {
				t.Fatalf("rate=%s index %d: %v", rate.ID(), i, err)
			}
			if IsDroppedLabel(rate, m, s, f) {
				t.Fatalf("rate=%s index %d maps to dropped label %02d:%02d:%02d;%02d",
					rate.ID(), i, h, m, s, f)
			}
			if back := FrameIndex(rate, h, m, s, f); back != i {
				t.Fatalf("rate=%s round trip broken: %d -> %02d:%02d:%02d;%02d -> %d",
					rate.ID(), i, h, m, s, f, back)
			}
			cur := label(h, m, s, f)
			if cur <= prev {
				t.Fatalf("rate=%s labels not strictly increasing at index %d: %q after %q",
					rate.ID(), i, cur, prev)
			}
			prev = cur
		}
	}
}

// TestMinuteContinuity checks that consecutive minutes map to consecutive
// frame ranges, including across ten-minute boundaries.
func TestMinuteContinuity(t *testing.T) {
	for _, rate := range []Rate{Rate2997, Rate5994} {
		prevLast := int64(-1)
		for minute := 0; minute < 24*60; minute++ {
			h, m := minute/60, minute%60
			firstFF := 0
			framesInMinute := int64(60 * rate.NominalFPS())
			if m%10 != 0 {
				firstFF = rate.DropCount()
				framesInMinute -= int64(rate.DropCount())
			}
			idx, err := FrameIndexFromTimecode(rate, label(h, m, 0, firstFF))
			if err != nil {
				t.Fatalf("rate=%s minute %d first label: %v", rate.ID(), m, err)
			}
			if idx != prevLast+1 {
				t.Fatalf("rate=%s discontinuity at %02d:%02d: index %d after %d",
					rate.ID(), h, m, idx, prevLast)
			}
			prevLast = idx + framesInMinute - 1
		}
		if prevLast != MaxFrameIndex(rate) {
			t.Fatalf("rate=%s last frame of day = %d, want %d", rate.ID(), prevLast, MaxFrameIndex(rate))
		}
	}
}

func label(h, m, s, f int) string {
	digits := "0123456789"
	b := []byte{
		digits[h/10], digits[h%10], ':',
		digits[m/10], digits[m%10], ':',
		digits[s/10], digits[s%10], ';',
		digits[f/10], digits[f%10],
	}
	return string(b)
}
