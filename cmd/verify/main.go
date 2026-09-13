// Command verify is a one-shot acceptance client: it waits for the API to
// become healthy, exercises boundary vectors, drop-frame rules, timecode
// spans and round-trip reversibility over HTTP, then exits non-zero on any
// failure.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// rateSpec captures the expected behaviour of one supported rate.
type rateSpec struct {
	id   string // canonical rate string
	fps  int    // nominal frames per second
	drop int    // labels skipped per dropped minute
	max  int64  // last legal frame index of the day
}

var (
	rate30 = rateSpec{id: "30000/1001", fps: 30, drop: 2, max: 2589407}
	rate60 = rateSpec{id: "60000/1001", fps: 60, drop: 4, max: 5178815}
	rates  = []rateSpec{rate30, rate60}
)

var (
	baseURL  = "http://localhost:8080"
	client   = &http.Client{Timeout: 5 * time.Second}
	failures int
)

func main() {
	if v := os.Getenv("API_BASE_URL"); v != "" {
		baseURL = v
	}
	waitHealthy()

	checkBoundaryVectors()
	checkDropFrameRules()
	checkMinuteContinuity()
	checkErrorEnvelope()
	checkTimecodeSpan()
	checkTimecodeOffset()
	checkTimecodeRetime()
	checkCaseVariantAmbiguity()
	checkRoundTrip()

	if failures > 0 {
		fmt.Printf("VERIFY FAILED: %d check(s) failed\n", failures)
		os.Exit(1)
	}
	fmt.Println("VERIFY OK: all acceptance checks passed")
}

func waitHealthy() {
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := client.Get(baseURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			fmt.Println("VERIFY FAILED: API did not become healthy within 60s")
			os.Exit(1)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// convert performs one conversion call and returns status plus decoded body.
func convert(payload map[string]any) (int, map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		fail("marshal request: %v", err)
		return 0, nil
	}
	resp, err := client.Post(baseURL+"/api/v1/convert", "application/json", bytes.NewReader(body))
	if err != nil {
		fail("POST /api/v1/convert: %v", err)
		return 0, nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		fail("read response: %v", err)
		return 0, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		fail("response is not JSON: %v (body: %s)", err, raw)
		return resp.StatusCode, nil
	}
	return resp.StatusCode, decoded
}

// convertRaw performs one conversion call with a raw JSON body and returns
// status plus decoded body.
func convertRaw(raw string) (int, map[string]any) {
	resp, err := client.Post(baseURL+"/api/v1/convert", "application/json", bytes.NewReader([]byte(raw)))
	if err != nil {
		fail("POST /api/v1/convert: %v", err)
		return 0, nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		fail("read response: %v", err)
		return resp.StatusCode, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		fail("response is not JSON: %v (body: %s)", err, data)
		return resp.StatusCode, nil
	}
	return resp.StatusCode, decoded
}

func fail(format string, args ...any) {
	failures++
	fmt.Printf("FAIL: %s\n", fmt.Sprintf(format, args...))
}

func pass(format string, args ...any) {
	fmt.Printf("ok: %s\n", fmt.Sprintf(format, args...))
}

// expectFrame asserts a forward (timecode -> frame_index) conversion.
func expectFrame(rate rateSpec, tc string, want int64) {
	status, body := convert(map[string]any{
		"direction": "timecode_to_frame", "rate": rate.id, "timecode": tc,
	})
	got, ok := body["frame_index"].(float64)
	if status != http.StatusOK || !ok || int64(got) != want {
		fail("rate=%s timecode=%s: want frame_index=%d, got status=%d body=%v", rate.id, tc, want, status, body)
		return
	}
	pass("rate=%s %s -> frame_index=%d", rate.id, tc, want)
}

// expectTimecode asserts a reverse (frame_index -> timecode) conversion.
func expectTimecode(rate rateSpec, index int64, want string) {
	status, body := convert(map[string]any{
		"direction": "frame_to_timecode", "rate": rate.id, "frame_index": index,
	})
	got, ok := body["timecode"].(string)
	if status != http.StatusOK || !ok || got != want {
		fail("rate=%s frame_index=%d: want timecode=%s, got status=%d body=%v", rate.id, index, want, status, body)
		return
	}
	pass("rate=%s frame_index=%d -> %s", rate.id, index, want)
}

// expectError asserts a 422 with a stable code and field, and no partial result.
func expectError(name string, payload map[string]any, wantCode, wantField string) {
	status, body := convert(payload)
	errObj, _ := body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	field, _ := errObj["field"].(string)
	_, leakedFrame := body["frame_index"]
	_, leakedTC := body["timecode"]
	_, leakedSpan := body["elapsed_frames"]
	_, leakedDay := body["day_offset"]
	if status != http.StatusUnprocessableEntity || code != wantCode || field != wantField ||
		leakedFrame || leakedTC || leakedSpan || leakedDay {
		fail("%s: want 422 code=%s field=%s without partial result, got status=%d body=%v",
			name, wantCode, wantField, status, body)
		return
	}
	pass("%s: 422 %s on field %q", name, code, field)
}

// expectSpan asserts a timecode_span conversion returns the expected
// elapsed frame count.
func expectSpan(name string, rate rateSpec, start, end string, nextDay bool, want int64) {
	payload := map[string]any{
		"direction": "timecode_span", "rate": rate.id,
		"start_timecode": start, "end_timecode": end,
	}
	if nextDay {
		payload["next_day"] = true
	}
	status, body := convert(payload)
	got, ok := body["elapsed_frames"].(float64)
	if status != http.StatusOK || !ok || int64(got) != want {
		fail("%s: want elapsed_frames=%d, got status=%d body=%v", name, want, status, body)
		return
	}
	pass("%s: elapsed_frames=%d", name, want)
}

func checkBoundaryVectors() {
	fmt.Println("--- boundary vectors ---")
	// Nominal 30 fps (29.97): drop 2 labels per non-tenth minute.
	expectFrame(rate30, "00:00:00;00", 0)
	expectFrame(rate30, "00:00:59;29", 1799)
	expectFrame(rate30, "00:01:00;02", 1800)
	expectFrame(rate30, "00:09:59;29", 17981)
	expectFrame(rate30, "00:10:00;00", 17982)
	expectFrame(rate30, "00:10:00;01", 17983)
	expectFrame(rate30, "01:00:00;02", 107894)
	expectFrame(rate30, "23:59:59;29", 2589407)
	expectTimecode(rate30, 0, "00:00:00;00")
	expectTimecode(rate30, 1800, "00:01:00;02")
	expectTimecode(rate30, 17982, "00:10:00;00")
	expectTimecode(rate30, 2589407, "23:59:59;29")

	// Nominal 60 fps (59.94): drop 4 labels per non-tenth minute.
	expectFrame(rate60, "00:00:00;00", 0)
	expectFrame(rate60, "00:00:59;59", 3599)
	expectFrame(rate60, "00:01:00;04", 3600)
	expectFrame(rate60, "00:09:59;59", 35963)
	expectFrame(rate60, "00:10:00;00", 35964)
	expectFrame(rate60, "01:00:00;04", 215788)
	expectFrame(rate60, "23:59:59;59", 5178815)
	expectTimecode(rate60, 0, "00:00:00;00")
	expectTimecode(rate60, 3600, "00:01:00;04")
	expectTimecode(rate60, 35964, "00:10:00;00")
	expectTimecode(rate60, 5178815, "23:59:59;59")
}

func checkDropFrameRules() {
	fmt.Println("--- dropped labels are illegal ---")
	for _, rate := range rates {
		for _, minute := range []int{1, 5, 9, 11, 23, 59} {
			for f := 0; f < rate.drop; f++ {
				tc := fmt.Sprintf("00:%02d:00;%02d", minute, f)
				expectError(fmt.Sprintf("rate=%s dropped label %s", rate.id, tc),
					map[string]any{"direction": "timecode_to_frame", "rate": rate.id, "timecode": tc},
					"DROPPED_FRAME_LABEL", "timecode")
			}
		}
		// Minutes divisible by ten keep all labels, including the low ones.
		for _, minute := range []int{0, 10, 20, 30, 40, 50} {
			tc := fmt.Sprintf("00:%02d:00;%02d", minute, rate.drop-1)
			status, body := convert(map[string]any{
				"direction": "timecode_to_frame", "rate": rate.id, "timecode": tc,
			})
			if status != http.StatusOK {
				fail("rate=%s label %s at ten-minute boundary must be legal, got status=%d body=%v",
					rate.id, tc, status, body)
			} else {
				pass("rate=%s ten-minute boundary label %s is legal", rate.id, tc)
			}
		}
	}
}

// checkMinuteContinuity walks every minute of the day and verifies the first
// legal label of each minute maps to the frame right after the last frame of
// the previous minute, so ten-minute boundaries stay continuous.
func checkMinuteContinuity() {
	fmt.Println("--- minute-to-minute continuity ---")
	for _, rate := range rates {
		prevLast := int64(-1)
		broken := false
		for minute := 0; minute < 24*60 && !broken; minute++ {
			h, m := minute/60, minute%60
			firstFF := 0
			framesInMinute := int64(60 * rate.fps)
			if m%10 != 0 {
				firstFF = rate.drop
				framesInMinute -= int64(rate.drop)
			}
			tc := fmt.Sprintf("%02d:%02d:00;%02d", h, m, firstFF)
			status, body := convert(map[string]any{
				"direction": "timecode_to_frame", "rate": rate.id, "timecode": tc,
			})
			idx, ok := body["frame_index"].(float64)
			if status != http.StatusOK || !ok {
				fail("rate=%s first legal label %s rejected: status=%d body=%v", rate.id, tc, status, body)
				broken = true
				continue
			}
			if int64(idx) != prevLast+1 {
				fail("rate=%s discontinuity at %s: frame_index=%d, previous minute ended at %d",
					rate.id, tc, int64(idx), prevLast)
				broken = true
				continue
			}
			prevLast = int64(idx) + framesInMinute - 1
		}
		if !broken {
			pass("rate=%s: all 1440 minute transitions are continuous", rate.id)
		}
	}
}

func checkErrorEnvelope() {
	fmt.Println("--- error envelope ---")
	expectError("negative frame_index",
		map[string]any{"direction": "frame_to_timecode", "rate": rate30.id, "frame_index": -1},
		"FRAME_INDEX_OUT_OF_RANGE", "frame_index")
	expectError("frame_index past end of day (30000/1001)",
		map[string]any{"direction": "frame_to_timecode", "rate": rate30.id, "frame_index": rate30.max + 1},
		"FRAME_INDEX_OUT_OF_RANGE", "frame_index")
	expectError("frame_index past end of day (60000/1001)",
		map[string]any{"direction": "frame_to_timecode", "rate": rate60.id, "frame_index": rate60.max + 1},
		"FRAME_INDEX_OUT_OF_RANGE", "frame_index")
	expectError("unsupported rate",
		map[string]any{"direction": "timecode_to_frame", "rate": "25", "timecode": "00:00:00;00"},
		"INVALID_RATE", "rate")
	expectError("unknown direction",
		map[string]any{"direction": "sideways", "rate": rate30.id, "timecode": "00:00:00;00"},
		"INVALID_DIRECTION", "direction")
	expectError("hour out of range",
		map[string]any{"direction": "timecode_to_frame", "rate": rate30.id, "timecode": "24:00:00;00"},
		"INVALID_TIMECODE_FORMAT", "timecode")
	expectError("frame digits out of range for rate",
		map[string]any{"direction": "timecode_to_frame", "rate": rate30.id, "timecode": "00:00:00;30"},
		"INVALID_TIMECODE_FORMAT", "timecode")
	expectError("wrong separator",
		map[string]any{"direction": "timecode_to_frame", "rate": rate30.id, "timecode": "00:00:00:00"},
		"INVALID_TIMECODE_FORMAT", "timecode")
	expectError("missing timecode",
		map[string]any{"direction": "timecode_to_frame", "rate": rate30.id},
		"MISSING_FIELD", "timecode")
	expectError("missing frame_index",
		map[string]any{"direction": "frame_to_timecode", "rate": rate30.id},
		"MISSING_FIELD", "frame_index")
}

// checkTimecodeSpan exercises the timecode_span direction: same-day spans
// over ten-minute boundaries, midnight rollover at both rates, zero-length
// spans, and rejection of an unauthorized day rollover.
func checkTimecodeSpan() {
	fmt.Println("--- timecode span ---")
	// Same-day spans across a ten-minute boundary (labels stay continuous).
	expectSpan("rate=30000/1001 same-day ten-minute boundary", rate30,
		"00:09:59;29", "00:10:00;01", false, 2)
	expectSpan("rate=60000/1001 same-day ten-minute boundary", rate60,
		"00:09:59;59", "00:10:00;03", false, 4)

	// Identical endpoints span zero frames.
	expectSpan("rate=30000/1001 same frame", rate30,
		"00:10:00;00", "00:10:00;00", false, 0)
	expectSpan("rate=60000/1001 same frame", rate60,
		"01:00:00;04", "01:00:00;04", false, 0)

	// Midnight rollover, one day at most, only with next_day=true.
	expectSpan("rate=30000/1001 across midnight", rate30,
		"23:59:59;29", "00:00:00;00", true, 1)
	expectSpan("rate=30000/1001 across midnight into morning", rate30,
		"23:59:00;02", "00:01:00;02", true, 3598)
	expectSpan("rate=60000/1001 across midnight", rate60,
		"23:59:59;59", "00:00:00;01", true, 2)
	expectSpan("rate=60000/1001 across midnight into morning", rate60,
		"23:59:00;04", "00:01:00;04", true, 7196)

	// End before start without next_day is rejected and carries no span.
	expectError("end before start without next_day (30000/1001)",
		map[string]any{"direction": "timecode_span", "rate": rate30.id,
			"start_timecode": "23:59:59;29", "end_timecode": "00:00:00;00"},
		"END_BEFORE_START", "end_timecode")
	expectError("end before start without next_day (60000/1001)",
		map[string]any{"direction": "timecode_span", "rate": rate60.id,
			"start_timecode": "23:59:59;59", "end_timecode": "00:00:00;00"},
		"END_BEFORE_START", "end_timecode")

	// Field-level validation matches the other directions.
	expectError("span missing end_timecode",
		map[string]any{"direction": "timecode_span", "rate": rate30.id,
			"start_timecode": "00:10:00;00"},
		"MISSING_FIELD", "end_timecode")
	expectError("span dropped start label",
		map[string]any{"direction": "timecode_span", "rate": rate30.id,
			"start_timecode": "00:01:00;01", "end_timecode": "00:10:00;00"},
		"DROPPED_FRAME_LABEL", "start_timecode")
	expectError("span bad end format",
		map[string]any{"direction": "timecode_span", "rate": rate30.id,
			"start_timecode": "00:10:00;00", "end_timecode": "00:10:00:00"},
		"INVALID_TIMECODE_FORMAT", "end_timecode")
}

// expectOffset asserts a timecode_offset conversion returns the expected
// target label and day_offset (-1 previous day, 0 same day, +1 next day).
func expectOffset(name string, rate rateSpec, tc string, offset int64, wantTC string, wantDay int) {
	status, body := convert(map[string]any{
		"direction": "timecode_offset", "rate": rate.id,
		"timecode": tc, "frame_offset": offset,
	})
	gotTC, okTC := body["timecode"].(string)
	gotDay, okDay := body["day_offset"].(float64)
	if status != http.StatusOK || !okTC || !okDay || gotTC != wantTC || int(gotDay) != wantDay {
		fail("%s: want timecode=%s day_offset=%d, got status=%d body=%v",
			name, wantTC, wantDay, status, body)
		return
	}
	pass("%s: %s day_offset=%d", name, gotTC, wantDay)
}

// checkTimecodeOffset exercises the timecode_offset direction at both
// rates: zero offsets, an editing point moved back across a ten-minute
// boundary, the last frame stepping into the first frame of the next day,
// a step back into the previous day, and rejection past an adjacent day.
func checkTimecodeOffset() {
	fmt.Println("--- timecode offset ---")
	for _, rate := range rates {
		_, lastBody := convert(map[string]any{
			"direction": "frame_to_timecode", "rate": rate.id, "frame_index": rate.max,
		})
		lastLabel, _ := lastBody["timecode"].(string)
		firstFF := "00"
		lastFF := fmt.Sprintf("%02d", rate.fps-1)

		// Zero offset returns the original label and day_offset 0.
		expectOffset(fmt.Sprintf("rate=%s zero offset mid-day", rate.id), rate,
			"00:10:00;00", 0, "00:10:00;00", 0)
		expectOffset(fmt.Sprintf("rate=%s zero offset at day edge", rate.id), rate,
			lastLabel, 0, lastLabel, 0)

		// Editing point moved one frame earlier across a ten-minute boundary.
		expectOffset(fmt.Sprintf("rate=%s ten-minute boundary moved back one frame", rate.id), rate,
			"00:10:00;00", -1, "00:09:59;"+lastFF, 0)

		// Last frame of the day shifted one frame later lands on the first
		// frame of the next day; the inverse step lands on the previous day.
		expectOffset(fmt.Sprintf("rate=%s last frame crosses to next day first frame", rate.id), rate,
			lastLabel, 1, "00:00:00;"+firstFF, 1)
		expectOffset(fmt.Sprintf("rate=%s first frame crosses to previous day last frame", rate.id), rate,
			"00:00:00;00", -1, lastLabel, -1)

		// Offsets past the adjacent natural day are rejected with no target.
		expectError(fmt.Sprintf("rate=%s offset past next day", rate.id),
			map[string]any{"direction": "timecode_offset", "rate": rate.id,
				"timecode": lastLabel, "frame_offset": rate.max + 2},
			"OFFSET_OUT_OF_RANGE", "frame_offset")
		expectError(fmt.Sprintf("rate=%s offset past previous day", rate.id),
			map[string]any{"direction": "timecode_offset", "rate": rate.id,
				"timecode": "00:00:00;00", "frame_offset": -(rate.max + 2)},
			"OFFSET_OUT_OF_RANGE", "frame_offset")
	}

	// Non-integer offsets and trailing JSON are rejected by strict parsing.
	if st, b := convertRaw(`{"direction":"timecode_offset","rate":"30000/1001","timecode":"00:10:00;00","frame_offset":1.5}`); st != http.StatusBadRequest {
		fail("non-integer frame_offset: want 400, got status=%d body=%v", st, b)
	} else {
		pass("non-integer frame_offset rejected with 400 MALFORMED_JSON")
	}
	if st, b := convertRaw(`{"direction":"timecode_offset","rate":"30000/1001","timecode":"00:10:00;00","frame_offset":-1}{"x":1}`); st != http.StatusBadRequest {
		fail("trailing JSON: want 400, got status=%d body=%v", st, b)
	} else {
		pass("trailing JSON rejected with 400 MALFORMED_JSON")
	}
}

// expectRetime asserts a timecode_retime conversion maps the source label to
// the expected target label.
func expectRetime(name, sourceRate, targetRate, tc, want string) {
	status, body := convert(map[string]any{
		"direction": "timecode_retime", "source_rate": sourceRate,
		"target_rate": targetRate, "timecode": tc,
	})
	got, ok := body["timecode"].(string)
	if status != http.StatusOK || !ok || got != want {
		fail("%s: want timecode=%s, got status=%d body=%v", name, want, status, body)
		return
	}
	pass("%s: %s -> %s", name, tc, want)
}

// checkTimecodeRetime exercises the timecode_retime direction: same-rate
// passthrough, the bidirectional mapping across the ten-minute boundary,
// exact 30 -> 60 -> 30 round trips, rejection of half-frame positions, and
// field-level validation.
func checkTimecodeRetime() {
	fmt.Println("--- timecode retime ---")
	// Same source and target rate returns the label unchanged.
	expectRetime("same rate 30000/1001", rate30.id, rate30.id, "00:10:00;00", "00:10:00;00")
	expectRetime("same rate 60000/1001", rate60.id, rate60.id, "00:10:00;00", "00:10:00;00")

	// Bidirectional mapping across the ten-minute boundary.
	expectRetime("30 -> 60 last frame before ten-minute boundary",
		rate30.id, rate60.id, "00:09:59;29", "00:09:59;58")
	expectRetime("30 -> 60 ten-minute boundary",
		rate30.id, rate60.id, "00:10:00;00", "00:10:00;00")
	expectRetime("60 -> 30 aligned frame before ten-minute boundary",
		rate60.id, rate30.id, "00:09:59;58", "00:09:59;29")
	expectRetime("60 -> 30 ten-minute boundary",
		rate60.id, rate30.id, "00:10:00;00", "00:10:00;00")

	// A 30 fps locate point survives the round trip 30 -> 60 -> 30 exactly.
	status, body := convert(map[string]any{
		"direction": "timecode_retime", "source_rate": rate30.id,
		"target_rate": rate60.id, "timecode": "00:09:59;29",
	})
	mid, _ := body["timecode"].(string)
	status2, body2 := convert(map[string]any{
		"direction": "timecode_retime", "source_rate": rate60.id,
		"target_rate": rate30.id, "timecode": mid,
	})
	back, _ := body2["timecode"].(string)
	if status != http.StatusOK || status2 != http.StatusOK || back != "00:09:59;29" {
		fail("30 -> 60 -> 30 round trip: want 00:09:59;29, got mid=%q back=%q (bodies %v, %v)",
			mid, back, body, body2)
	} else {
		pass("30 -> 60 -> 30 round trip: 00:09:59;29 -> %s -> %s", mid, back)
	}

	// A 60 fps label on a half-frame position of the 30 fps grid is rejected
	// and carries no target value.
	expectError("60 -> 30 half-frame position",
		map[string]any{"direction": "timecode_retime", "source_rate": rate60.id,
			"target_rate": rate30.id, "timecode": "00:10:00;01"},
		"TIMECODE_NOT_ALIGNED", "timecode")
	expectError("60 -> 30 last frame of day is a half-frame position",
		map[string]any{"direction": "timecode_retime", "source_rate": rate60.id,
			"target_rate": rate30.id, "timecode": "23:59:59;59"},
		"TIMECODE_NOT_ALIGNED", "timecode")

	// Field-level validation matches the other directions.
	expectError("retime invalid source_rate",
		map[string]any{"direction": "timecode_retime", "source_rate": "25",
			"target_rate": rate60.id, "timecode": "00:10:00;00"},
		"INVALID_RATE", "source_rate")
	expectError("retime invalid target_rate",
		map[string]any{"direction": "timecode_retime", "source_rate": rate30.id,
			"target_rate": "29.97", "timecode": "00:10:00;00"},
		"INVALID_RATE", "target_rate")
	expectError("retime missing timecode",
		map[string]any{"direction": "timecode_retime", "source_rate": rate30.id,
			"target_rate": rate60.id},
		"MISSING_FIELD", "timecode")
	expectError("retime dropped source label",
		map[string]any{"direction": "timecode_retime", "source_rate": rate30.id,
			"target_rate": rate60.id, "timecode": "00:01:00;01"},
		"DROPPED_FRAME_LABEL", "timecode")
}

// checkCaseVariantAmbiguity exercises requests that supply one logical field
// under two differently cased spellings (encoding/json binds both to the same
// struct field). Conflicting meanings must be rejected as ambiguous, in both
// key orders, including an illegal/legal timecode pair that must not migrate.
func checkCaseVariantAmbiguity() {
	fmt.Println("--- case-variant field ambiguity ---")
	expectAmbiguousRaw := func(name, raw, wantField string) {
		status, body := convertRaw(raw)
		errObj, _ := body["error"].(map[string]any)
		code, _ := errObj["code"].(string)
		field, _ := errObj["field"].(string)
		_, leakedTC := body["timecode"]
		if status != http.StatusUnprocessableEntity || code != "AMBIGUOUS_FIELD" || field != wantField || leakedTC {
			fail("%s: want 422 AMBIGUOUS_FIELD on field %q without a result, got status=%d body=%v",
				name, wantField, status, body)
			return
		}
		pass("%s: 422 AMBIGUOUS_FIELD on field %q", name, wantField)
	}
	expectAmbiguousRaw("direction variants in conflict, canonical last",
		`{"Direction":"timecode_to_frame","direction":"timecode_retime",`+
			`"source_rate":"30000/1001","target_rate":"60000/1001","timecode":"00:10:00;00"}`,
		"direction")
	expectAmbiguousRaw("direction variants in conflict, capital last",
		`{"direction":"timecode_retime","Direction":"timecode_to_frame",`+
			`"source_rate":"30000/1001","target_rate":"60000/1001","timecode":"00:10:00;00"}`,
		"direction")
	expectAmbiguousRaw("source_rate variants in conflict, capital last",
		`{"direction":"timecode_retime","source_rate":"30000/1001","Source_Rate":"60000/1001",`+
			`"target_rate":"60000/1001","timecode":"00:10:00;00"}`,
		"source_rate")
	expectAmbiguousRaw("target_rate variants in conflict, capital last",
		`{"direction":"timecode_retime","source_rate":"30000/1001",`+
			`"target_rate":"60000/1001","Target_Rate":"30000/1001","timecode":"00:10:00;00"}`,
		"target_rate")
	expectAmbiguousRaw("timecode variants: illegal and legal labels, capital last",
		`{"direction":"timecode_retime","source_rate":"30000/1001","target_rate":"60000/1001",`+
			`"timecode":"00:01:00;00","Timecode":"00:10:00;00"}`,
		"timecode")
	expectAmbiguousRaw("timecode variants: legal then illegal, canonical last",
		`{"direction":"timecode_retime","source_rate":"30000/1001","target_rate":"60000/1001",`+
			`"Timecode":"00:10:00;00","timecode":"00:01:00;00"}`,
		"timecode")

	// Same value under two spellings is a harmless repeat and still migrates.
	status, body := convertRaw(`{"direction":"timecode_retime","source_rate":"30000/1001",` +
		`"Source_Rate":"30000/1001","target_rate":"60000/1001","timecode":"00:10:00;00"}`)
	if status != http.StatusOK || body["timecode"] != "00:10:00;00" {
		fail("same-value case variant: want 200 migrated label, got status=%d body=%v", status, body)
	} else {
		pass("same-value case variant accepted")
	}
}

func checkRoundTrip() {
	fmt.Println("--- round-trip reversibility ---")
	for _, rate := range rates {
		// Deterministic stride sample across the whole day plus the edges.
		indices := []int64{0, 1, 2, rate.max - 1, rate.max}
		for i := int64(0); i <= rate.max; i += 9973 {
			indices = append(indices, i)
		}
		bad := 0
		for _, idx := range indices {
			status, body := convert(map[string]any{
				"direction": "frame_to_timecode", "rate": rate.id, "frame_index": idx,
			})
			tc, ok := body["timecode"].(string)
			if status != http.StatusOK || !ok {
				fail("rate=%s frame_index=%d reverse failed: status=%d body=%v", rate.id, idx, status, body)
				bad++
				continue
			}
			status2, body2 := convert(map[string]any{
				"direction": "timecode_to_frame", "rate": rate.id, "timecode": tc,
			})
			back, ok := body2["frame_index"].(float64)
			if status2 != http.StatusOK || !ok || int64(back) != idx {
				fail("rate=%s round trip broken: %d -> %s -> status=%d body=%v", rate.id, idx, tc, status2, body2)
				bad++
			}
		}
		if bad == 0 {
			pass("rate=%s: %d sampled indices round-trip exactly", rate.id, len(indices))
		}
	}
}
