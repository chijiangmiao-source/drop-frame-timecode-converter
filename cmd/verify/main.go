// Command verify is a one-shot acceptance client: it waits for the API to
// become healthy, exercises boundary vectors, drop-frame rules and
// round-trip reversibility over HTTP, then exits non-zero on any failure.
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
	if status != http.StatusUnprocessableEntity || code != wantCode || field != wantField || leakedFrame || leakedTC {
		fail("%s: want 422 code=%s field=%s without partial result, got status=%d body=%v",
			name, wantCode, wantField, status, body)
		return
	}
	pass("%s: 422 %s on field %q", name, code, field)
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
