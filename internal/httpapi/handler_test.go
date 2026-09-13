package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

func doConvert(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	var err error
	switch v := body.(type) {
	case string:
		raw = []byte(v)
	default:
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/convert", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	NewRouter().ServeHTTP(w, req)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decoded), "response must be JSON: %q", w.Body.String())
	return w.Code, decoded
}

func TestTimecodeToFrameBoundaryVectors(t *testing.T) {
	cases := []struct {
		name string
		rate string
		tc   string
		want int64
	}{
		{"30 first frame of day", "30000/1001", "00:00:00;00", 0},
		{"30 last frame of minute 0", "30000/1001", "00:00:59;29", 1799},
		{"30 first legal label of minute 1", "30000/1001", "00:01:00;02", 1800},
		{"30 last frame before ten-minute boundary", "30000/1001", "00:09:59;29", 17981},
		{"30 ten-minute boundary", "30000/1001", "00:10:00;00", 17982},
		{"30 ten-minute boundary plus one", "30000/1001", "00:10:00;01", 17983},
		{"30 one hour mark", "30000/1001", "01:00:00;02", 107894},
		{"30 last frame of day", "30000/1001", "23:59:59;29", 2589407},
		{"60 first frame of day", "60000/1001", "00:00:00;00", 0},
		{"60 last frame of minute 0", "60000/1001", "00:00:59;59", 3599},
		{"60 first legal label of minute 1", "60000/1001", "00:01:00;04", 3600},
		{"60 last frame before ten-minute boundary", "60000/1001", "00:09:59;59", 35963},
		{"60 ten-minute boundary", "60000/1001", "00:10:00;00", 35964},
		{"60 one hour mark", "60000/1001", "01:00:00;04", 215788},
		{"60 last frame of day", "60000/1001", "23:59:59;59", 5178815},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "timecode_to_frame", "rate": c.rate, "timecode": c.tc,
			})
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, float64(c.want), body["frame_index"])
			assert.NotContains(t, body, "timecode")
			assert.NotContains(t, body, "error")
		})
	}
}

func TestFrameToTimecodeBoundaryVectors(t *testing.T) {
	cases := []struct {
		name  string
		rate  string
		index int64
		want  string
	}{
		{"30 index zero", "30000/1001", 0, "00:00:00;00"},
		{"30 into dropped minute", "30000/1001", 1800, "00:01:00;02"},
		{"30 ten-minute boundary", "30000/1001", 17982, "00:10:00;00"},
		{"30 last of day", "30000/1001", 2589407, "23:59:59;29"},
		{"60 index zero", "60000/1001", 0, "00:00:00;00"},
		{"60 into dropped minute", "60000/1001", 3600, "00:01:00;04"},
		{"60 ten-minute boundary", "60000/1001", 35964, "00:10:00;00"},
		{"60 last of day", "60000/1001", 5178815, "23:59:59;59"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "frame_to_timecode", "rate": c.rate, "frame_index": c.index,
			})
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, c.want, body["timecode"])
			assert.NotContains(t, body, "frame_index")
			assert.NotContains(t, body, "error")
		})
	}
}

func TestDroppedLabelsRejected(t *testing.T) {
	cases := []struct {
		name string
		rate string
		tc   string
	}{
		{"30 drops ;00", "30000/1001", "00:01:00;00"},
		{"30 drops ;01", "30000/1001", "00:01:00;01"},
		{"30 drops at minute 9", "30000/1001", "00:09:00;01"},
		{"30 drops at minute 11", "30000/1001", "00:11:00;00"},
		{"30 drops at minute 59", "30000/1001", "00:59:00;01"},
		{"60 drops ;00", "60000/1001", "00:01:00;00"},
		{"60 drops ;01", "60000/1001", "00:01:00;01"},
		{"60 drops ;02", "60000/1001", "00:01:00;02"},
		{"60 drops ;03", "60000/1001", "00:01:00;03"},
		{"60 drops at minute 59", "60000/1001", "00:59:00;03"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "timecode_to_frame", "rate": c.rate, "timecode": c.tc,
			})
			assertError(t, status, body, "DROPPED_FRAME_LABEL", "timecode")
		})
	}
}

func TestTenMinuteBoundaryLabelsAccepted(t *testing.T) {
	for _, rate := range []struct {
		id      string
		lastLow string
	}{
		{"30000/1001", "00:10:00;01"},
		{"60000/1001", "00:10:00;03"},
	} {
		status, body := doConvert(t, map[string]any{
			"direction": "timecode_to_frame", "rate": rate.id, "timecode": rate.lastLow,
		})
		require.Equal(t, http.StatusOK, status, "label %s at ten-minute boundary must be legal", rate.lastLow)
		assert.Contains(t, body, "frame_index")
	}
}

func TestFrameIndexRangeErrors(t *testing.T) {
	cases := []struct {
		name  string
		rate  string
		index int64
	}{
		{"30 negative", "30000/1001", -1},
		{"30 beyond last frame", "30000/1001", 2589408},
		{"60 negative", "60000/1001", -1},
		{"60 beyond last frame", "60000/1001", 5178816},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "frame_to_timecode", "rate": c.rate, "frame_index": c.index,
			})
			assertError(t, status, body, "FRAME_INDEX_OUT_OF_RANGE", "frame_index")
		})
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name      string
		body      any
		wantCode  string
		wantField string
	}{
		{"unsupported rate", map[string]any{
			"direction": "timecode_to_frame", "rate": "25", "timecode": "00:00:00;00"},
			"INVALID_RATE", "rate"},
		{"empty rate", map[string]any{
			"direction": "timecode_to_frame", "rate": "", "timecode": "00:00:00;00"},
			"INVALID_RATE", "rate"},
		{"unknown direction", map[string]any{
			"direction": "sideways", "rate": "30000/1001", "timecode": "00:00:00;00"},
			"INVALID_DIRECTION", "direction"},
		{"missing direction", map[string]any{
			"rate": "30000/1001", "timecode": "00:00:00;00"},
			"INVALID_DIRECTION", "direction"},
		{"missing timecode", map[string]any{
			"direction": "timecode_to_frame", "rate": "30000/1001"},
			"MISSING_FIELD", "timecode"},
		{"missing frame_index", map[string]any{
			"direction": "frame_to_timecode", "rate": "30000/1001"},
			"MISSING_FIELD", "frame_index"},
		{"hour out of range", map[string]any{
			"direction": "timecode_to_frame", "rate": "30000/1001", "timecode": "24:00:00;00"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"minute out of range", map[string]any{
			"direction": "timecode_to_frame", "rate": "30000/1001", "timecode": "00:60:00;00"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"second out of range", map[string]any{
			"direction": "timecode_to_frame", "rate": "30000/1001", "timecode": "00:00:60;00"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"frame out of range for nominal 30", map[string]any{
			"direction": "timecode_to_frame", "rate": "30000/1001", "timecode": "00:00:00;30"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"frame out of range for nominal 60", map[string]any{
			"direction": "timecode_to_frame", "rate": "60000/1001", "timecode": "00:00:00;60"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"wrong separator", map[string]any{
			"direction": "timecode_to_frame", "rate": "30000/1001", "timecode": "00:00:00:00"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"non-numeric", map[string]any{
			"direction": "timecode_to_frame", "rate": "30000/1001", "timecode": "ab:cd:ef;gh"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			assertError(t, status, body, c.wantCode, c.wantField)
		})
	}
}

func TestTimecodeSpan(t *testing.T) {
	cases := []struct {
		name  string
		rate  string
		start string
		end   string
		next  bool
		want  int64
	}{
		{"30 same-day ten-minute boundary", "30000/1001", "00:09:59;29", "00:10:00;01", false, 2},
		{"60 same-day ten-minute boundary", "60000/1001", "00:09:59;59", "00:10:00;03", false, 4},
		{"30 same frame returns zero", "30000/1001", "00:10:00;00", "00:10:00;00", false, 0},
		{"60 same frame returns zero", "60000/1001", "01:00:00;04", "01:00:00;04", false, 0},
		{"30 across midnight", "30000/1001", "23:59:59;29", "00:00:00;00", true, 1},
		{"30 across midnight into morning", "30000/1001", "23:59:00;02", "00:01:00;02", true, 3598},
		{"60 across midnight", "60000/1001", "23:59:59;59", "00:00:00;01", true, 2},
		{"60 across midnight into morning", "60000/1001", "23:59:00;04", "00:01:00;04", true, 7196},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := map[string]any{
				"direction": "timecode_span", "rate": c.rate,
				"start_timecode": c.start, "end_timecode": c.end,
			}
			if c.next {
				body["next_day"] = true
			}
			status, resp := doConvert(t, body)
			require.Equal(t, http.StatusOK, status, "body: %v", resp)
			assert.Equal(t, float64(c.want), resp["elapsed_frames"])
			assert.NotContains(t, resp, "frame_index")
			assert.NotContains(t, resp, "timecode")
			assert.NotContains(t, resp, "error")
		})
	}
}

func TestTimecodeOffset(t *testing.T) {
	cases := []struct {
		name    string
		rate    string
		tc      string
		offset  int64
		wantTC  string
		wantDay int
	}{
		// Editing-point moves backwards across a ten-minute boundary.
		{"30 zero offset returns same label", "30000/1001", "00:10:00;00", 0, "00:10:00;00", 0},
		{"30 ten-minute boundary moved back one frame", "30000/1001", "00:10:00;00", -1, "00:09:59;29", 0},
		{"30 ten minutes back", "30000/1001", "00:10:00;00", -17982, "00:00:00;00", 0},
		{"30 last frame crosses to next day first frame", "30000/1001", "23:59:59;29", 1, "00:00:00;00", 1},
		{"30 first frame crosses to previous day last frame", "30000/1001", "00:00:00;00", -1, "23:59:59;29", -1},
		{"60 zero offset returns same label", "60000/1001", "00:10:00;00", 0, "00:10:00;00", 0},
		{"60 ten-minute boundary moved back one frame", "60000/1001", "00:10:00;00", -1, "00:09:59;59", 0},
		{"60 ten minutes back", "60000/1001", "00:10:00;00", -35964, "00:00:00;00", 0},
		{"60 last frame crosses to next day first frame", "60000/1001", "23:59:59;59", 1, "00:00:00;00", 1},
		{"60 first frame crosses to previous day last frame", "60000/1001", "00:00:00;00", -1, "23:59:59;59", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "timecode_offset", "rate": c.rate,
				"timecode": c.tc, "frame_offset": c.offset,
			})
			require.Equal(t, http.StatusOK, status, "body: %v", body)
			assert.Equal(t, c.wantTC, body["timecode"])
			assert.Equal(t, float64(c.wantDay), body["day_offset"])
			assert.NotContains(t, body, "frame_index")
			assert.NotContains(t, body, "elapsed_frames")
			assert.NotContains(t, body, "error")
		})
	}
}

func TestTimecodeOffsetOutOfRange(t *testing.T) {
	cases := []struct {
		name   string
		rate   string
		tc     string
		offset int64
	}{
		{"30 forward past next day", "30000/1001", "23:59:59;29", 2589409},
		{"30 backward past previous day", "30000/1001", "00:00:00;00", -2589409},
		{"60 forward past next day", "60000/1001", "23:59:59;59", 5178817},
		{"60 backward past previous day", "60000/1001", "00:00:00;00", -5178817},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "timecode_offset", "rate": c.rate,
				"timecode": c.tc, "frame_offset": c.offset,
			})
			assertError(t, status, body, "OFFSET_OUT_OF_RANGE", "frame_offset")
			// day_offset is a result field too and must not leak on failure.
			assert.NotContains(t, body, "day_offset")
		})
	}
}

func TestTimecodeOffsetFieldErrors(t *testing.T) {
	cases := []struct {
		name      string
		body      map[string]any
		wantCode  string
		wantField string
	}{
		{"missing timecode", map[string]any{
			"direction": "timecode_offset", "rate": "30000/1001", "frame_offset": 1},
			"MISSING_FIELD", "timecode"},
		{"missing frame_offset", map[string]any{
			"direction": "timecode_offset", "rate": "30000/1001", "timecode": "00:10:00;00"},
			"MISSING_FIELD", "frame_offset"},
		{"bad timecode format", map[string]any{
			"direction": "timecode_offset", "rate": "30000/1001",
			"timecode": "00:10:00:00", "frame_offset": 1},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"dropped timecode label", map[string]any{
			"direction": "timecode_offset", "rate": "30000/1001",
			"timecode": "00:01:00;01", "frame_offset": 1},
			"DROPPED_FRAME_LABEL", "timecode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			assertError(t, status, body, c.wantCode, c.wantField)
		})
	}
}

func TestTimecodeOffsetNonIntegerRejected(t *testing.T) {
	// JSON decoding into *int64 rejects fractional and non-numeric values.
	for _, raw := range []string{`1.5`, `"1"`, `true`} {
		body := `{"direction":"timecode_offset","rate":"30000/1001",` +
			`"timecode":"00:10:00;00","frame_offset":` + raw + `}`
		status, resp := doConvert(t, body)
		require.Equal(t, http.StatusBadRequest, status, "raw=%s body=%v", raw, resp)
		errObj, ok := resp["error"].(map[string]any)
		require.True(t, ok, "error envelope missing: %v", resp)
		assert.Equal(t, "MALFORMED_JSON", errObj["code"])
		assert.NotContains(t, resp, "timecode")
		assert.NotContains(t, resp, "day_offset")
	}
}

func TestTimecodeOffsetAmbiguousFieldsRejected(t *testing.T) {
	base := `"direction":"timecode_offset","rate":"30000/1001","timecode":"00:10:00;00","frame_offset":-1`
	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{"duplicate timecode with different values", `{` + base + `,"timecode":"00:10:00;01"}`, "timecode"},
		{"duplicate frame_offset with different values", `{` + base + `,"frame_offset":-2}`, "frame_offset"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			assertError(t, status, body, "AMBIGUOUS_FIELD", c.wantField)
		})
	}
}

func TestTimecodeOffsetDuplicateFieldWithSameValueAccepted(t *testing.T) {
	body := `{"direction":"timecode_offset","rate":"30000/1001","timecode":"00:10:00;00",` +
		`"frame_offset":-1,"frame_offset":-1}`
	status, resp := doConvert(t, body)
	require.Equal(t, http.StatusOK, status, "body: %v", resp)
	assert.Equal(t, "00:09:59;29", resp["timecode"])
	assert.Equal(t, float64(0), resp["day_offset"])
}

func TestTimecodeOffsetTrailingJSONRejected(t *testing.T) {
	body := `{"direction":"timecode_offset","rate":"30000/1001","timecode":"00:10:00;00",` +
		`"frame_offset":-1}{"direction":"timecode_to_frame"}`
	status, resp := doConvert(t, body)
	require.Equal(t, http.StatusBadRequest, status)
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %v", resp)
	assert.Equal(t, "MALFORMED_JSON", errObj["code"])
	assert.NotContains(t, resp, "timecode")
}

func TestTimecodeRetime(t *testing.T) {
	cases := []struct {
		name       string
		sourceRate string
		targetRate string
		tc         string
		want       string
	}{
		{"same rate 30 returns label unchanged", "30000/1001", "30000/1001", "00:10:00;00", "00:10:00;00"},
		{"same rate 60 returns label unchanged", "60000/1001", "60000/1001", "00:10:00;00", "00:10:00;00"},
		{"30 to 60 first frame of day", "30000/1001", "60000/1001", "00:00:00;00", "00:00:00;00"},
		{"30 to 60 last frame before ten-minute boundary", "30000/1001", "60000/1001", "00:09:59;29", "00:09:59;58"},
		{"30 to 60 ten-minute boundary", "30000/1001", "60000/1001", "00:10:00;00", "00:10:00;00"},
		{"30 to 60 one hour mark", "30000/1001", "60000/1001", "01:00:00;02", "01:00:00;04"},
		{"30 to 60 last frame of day", "30000/1001", "60000/1001", "23:59:59;29", "23:59:59;58"},
		{"60 to 30 first frame of day", "60000/1001", "30000/1001", "00:00:00;00", "00:00:00;00"},
		{"60 to 30 aligned frame before ten-minute boundary", "60000/1001", "30000/1001", "00:09:59;58", "00:09:59;29"},
		{"60 to 30 ten-minute boundary", "60000/1001", "30000/1001", "00:10:00;00", "00:10:00;00"},
		{"60 to 30 one hour mark", "60000/1001", "30000/1001", "01:00:00;04", "01:00:00;02"},
		{"60 to 30 last aligned frame of day", "60000/1001", "30000/1001", "23:59:59;58", "23:59:59;29"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "timecode_retime", "source_rate": c.sourceRate,
				"target_rate": c.targetRate, "timecode": c.tc,
			})
			require.Equal(t, http.StatusOK, status, "body: %v", body)
			assert.Equal(t, c.want, body["timecode"])
			assert.NotContains(t, body, "frame_index")
			assert.NotContains(t, body, "elapsed_frames")
			assert.NotContains(t, body, "error")
		})
	}
}

func TestTimecodeRetimeRoundTrip(t *testing.T) {
	// A 30 fps locate point survives the round trip 30 -> 60 -> 30 exactly.
	for _, tc := range []string{
		"00:00:00;00", "00:01:00;02", "00:09:59;29", "00:10:00;00",
		"00:10:00;01", "01:00:00;02", "23:59:59;29",
	} {
		status, body := doConvert(t, map[string]any{
			"direction": "timecode_retime", "source_rate": "30000/1001",
			"target_rate": "60000/1001", "timecode": tc,
		})
		require.Equal(t, http.StatusOK, status, "body: %v", body)
		mid, ok := body["timecode"].(string)
		require.True(t, ok)

		status2, body2 := doConvert(t, map[string]any{
			"direction": "timecode_retime", "source_rate": "60000/1001",
			"target_rate": "30000/1001", "timecode": mid,
		})
		require.Equal(t, http.StatusOK, status2, "body: %v", body2)
		assert.Equal(t, tc, body2["timecode"], "round trip %s -> %s", tc, mid)
	}
}

func TestTimecodeRetimeNotAligned(t *testing.T) {
	cases := []struct {
		name string
		tc   string
	}{
		{"odd frame at start of day", "00:00:00;01"},
		{"odd frame mid-minute", "00:05:23;45"},
		{"odd frame at ten-minute boundary", "00:10:00;01"},
		{"odd frame before ten-minute boundary", "00:09:59;59"},
		{"last frame of day is odd", "23:59:59;59"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, map[string]any{
				"direction": "timecode_retime", "source_rate": "60000/1001",
				"target_rate": "30000/1001", "timecode": c.tc,
			})
			assertError(t, status, body, "TIMECODE_NOT_ALIGNED", "timecode")
		})
	}
}

func TestTimecodeRetimeFieldErrors(t *testing.T) {
	cases := []struct {
		name      string
		body      map[string]any
		wantCode  string
		wantField string
	}{
		{"invalid source_rate", map[string]any{
			"direction": "timecode_retime", "source_rate": "25",
			"target_rate": "60000/1001", "timecode": "00:10:00;00"},
			"INVALID_RATE", "source_rate"},
		{"missing source_rate", map[string]any{
			"direction":   "timecode_retime",
			"target_rate": "60000/1001", "timecode": "00:10:00;00"},
			"INVALID_RATE", "source_rate"},
		{"invalid target_rate", map[string]any{
			"direction": "timecode_retime", "source_rate": "30000/1001",
			"target_rate": "29.97", "timecode": "00:10:00;00"},
			"INVALID_RATE", "target_rate"},
		{"missing target_rate", map[string]any{
			"direction": "timecode_retime", "source_rate": "30000/1001",
			"timecode": "00:10:00;00"},
			"INVALID_RATE", "target_rate"},
		{"missing timecode", map[string]any{
			"direction": "timecode_retime", "source_rate": "30000/1001",
			"target_rate": "60000/1001"},
			"MISSING_FIELD", "timecode"},
		{"bad timecode format", map[string]any{
			"direction": "timecode_retime", "source_rate": "30000/1001",
			"target_rate": "60000/1001", "timecode": "00:10:00:00"},
			"INVALID_TIMECODE_FORMAT", "timecode"},
		{"dropped source label", map[string]any{
			"direction": "timecode_retime", "source_rate": "30000/1001",
			"target_rate": "60000/1001", "timecode": "00:01:00;01"},
			"DROPPED_FRAME_LABEL", "timecode"},
		{"dropped source label same rate", map[string]any{
			"direction": "timecode_retime", "source_rate": "60000/1001",
			"target_rate": "60000/1001", "timecode": "00:01:00;03"},
			"DROPPED_FRAME_LABEL", "timecode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			assertError(t, status, body, c.wantCode, c.wantField)
		})
	}
}

func TestTimecodeRetimeAmbiguousFieldsRejected(t *testing.T) {
	base := `"direction":"timecode_retime","source_rate":"30000/1001","target_rate":"60000/1001","timecode":"00:10:00;00"`
	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{"duplicate source_rate with different values", `{` + base + `,"source_rate":"60000/1001"}`, "source_rate"},
		{"duplicate target_rate with different values", `{` + base + `,"target_rate":"30000/1001"}`, "target_rate"},
		{"duplicate timecode with different values", `{` + base + `,"timecode":"00:10:00;01"}`, "timecode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			assertError(t, status, body, "AMBIGUOUS_FIELD", c.wantField)
		})
	}
}

func TestTimecodeRetimeCaseVariantFieldsRejected(t *testing.T) {
	// encoding/json matches struct fields case-insensitively, so a canonical
	// key and a differently cased spelling ("source_rate" vs "Source_Rate")
	// populate the same field. Different values under the two spellings make
	// the request meaning ambiguous and must be rejected before conversion,
	// regardless of which spelling happens to be decoded last.
	base := `"source_rate":"30000/1001","target_rate":"60000/1001","timecode":"00:10:00;00"`
	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{
			name:      "case-variant directions with different meanings, canonical last",
			body:      `{"Direction":"timecode_to_frame","direction":"timecode_retime",` + base + `}`,
			wantField: "direction",
		},
		{
			name: "case-variant directions with different meanings, capital last",
			body: `{"direction":"timecode_retime","Direction":"timecode_to_frame",` +
				`"source_rate":"30000/1001","target_rate":"60000/1001","timecode":"00:10:00;00"}`,
			wantField: "direction",
		},
		{
			name: "case-variant source rates in conflict, canonical last",
			body: `{"direction":"timecode_retime","Source_Rate":"60000/1001","source_rate":"30000/1001",` +
				`"target_rate":"60000/1001","timecode":"00:10:00;00"}`,
			wantField: "source_rate",
		},
		{
			name: "case-variant source rates in conflict, capital last",
			body: `{"direction":"timecode_retime","source_rate":"30000/1001","Source_Rate":"60000/1001",` +
				`"target_rate":"60000/1001","timecode":"00:10:00;00"}`,
			wantField: "source_rate",
		},
		{
			name: "case-variant target rates in conflict, capital last",
			body: `{"direction":"timecode_retime","source_rate":"30000/1001",` +
				`"target_rate":"60000/1001","Target_Rate":"30000/1001","timecode":"00:10:00;00"}`,
			wantField: "target_rate",
		},
		{
			name: "case-variant timecodes, one illegal and one legal, capital last",
			body: `{"direction":"timecode_retime","source_rate":"30000/1001","target_rate":"60000/1001",` +
				`"timecode":"00:01:00;00","Timecode":"00:10:00;00"}`,
			wantField: "timecode",
		},
		{
			name: "case-variant timecodes, legal first then illegal, canonical last",
			body: `{"direction":"timecode_retime","source_rate":"30000/1001","target_rate":"60000/1001",` +
				`"Timecode":"00:10:00;00","timecode":"00:01:00;00"}`,
			wantField: "timecode",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			// The conflict must surface as ambiguity, never as a successful
			// migration or a downstream error such as DROPPED_FRAME_LABEL on
			// whichever value happened to be decoded last.
			assertError(t, status, body, "AMBIGUOUS_FIELD", c.wantField)
		})
	}
}

func TestTimecodeRetimeCaseVariantFieldWithSameValueAccepted(t *testing.T) {
	body := `{"direction":"timecode_retime","source_rate":"30000/1001","Source_Rate":"30000/1001",` +
		`"target_rate":"60000/1001","timecode":"00:10:00;00"}`
	status, resp := doConvert(t, body)
	require.Equal(t, http.StatusOK, status, "body: %v", resp)
	assert.Equal(t, "00:10:00;00", resp["timecode"])
}

func TestTimecodeRetimeTrailingJSONRejected(t *testing.T) {
	body := `{"direction":"timecode_retime","source_rate":"30000/1001",` +
		`"target_rate":"60000/1001","timecode":"00:10:00;00"}{"direction":"timecode_to_frame"}`
	status, resp := doConvert(t, body)
	require.Equal(t, http.StatusBadRequest, status)
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %v", resp)
	assert.Equal(t, "MALFORMED_JSON", errObj["code"])
	assert.NotContains(t, resp, "timecode")
}

func TestTimecodeSpanEndBeforeStartRejected(t *testing.T) {
	for _, rate := range []string{"30000/1001", "60000/1001"} {
		status, body := doConvert(t, map[string]any{
			"direction": "timecode_span", "rate": rate,
			"start_timecode": "23:59:59;29", "end_timecode": "00:00:00;00",
		})
		assertError(t, status, body, "END_BEFORE_START", "end_timecode")
	}
}

func TestTimecodeSpanFieldErrors(t *testing.T) {
	cases := []struct {
		name      string
		body      map[string]any
		wantCode  string
		wantField string
	}{
		{"missing start", map[string]any{
			"direction": "timecode_span", "rate": "30000/1001", "end_timecode": "00:10:00;00"},
			"MISSING_FIELD", "start_timecode"},
		{"missing end", map[string]any{
			"direction": "timecode_span", "rate": "30000/1001", "start_timecode": "00:10:00;00"},
			"MISSING_FIELD", "end_timecode"},
		{"bad start format", map[string]any{
			"direction": "timecode_span", "rate": "30000/1001",
			"start_timecode": "00:10:00:00", "end_timecode": "00:10:00;00"},
			"INVALID_TIMECODE_FORMAT", "start_timecode"},
		{"bad end format", map[string]any{
			"direction": "timecode_span", "rate": "30000/1001",
			"start_timecode": "00:10:00;00", "end_timecode": "24:00:00;00"},
			"INVALID_TIMECODE_FORMAT", "end_timecode"},
		{"dropped start label", map[string]any{
			"direction": "timecode_span", "rate": "30000/1001",
			"start_timecode": "00:01:00;01", "end_timecode": "00:10:00;00"},
			"DROPPED_FRAME_LABEL", "start_timecode"},
		{"dropped end label", map[string]any{
			"direction": "timecode_span", "rate": "60000/1001",
			"start_timecode": "00:10:00;00", "end_timecode": "00:11:00;03"},
			"DROPPED_FRAME_LABEL", "end_timecode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			assertError(t, status, body, c.wantCode, c.wantField)
		})
	}
}

func TestTimecodeSpanAmbiguousFieldsRejected(t *testing.T) {
	base := `"direction":"timecode_span","rate":"30000/1001","start_timecode":"23:59:59;29","end_timecode":"00:00:00;00","next_day":true`
	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{
			name:      "duplicate start with different values",
			body:      `{` + base + `,"start_timecode":"23:59:59;28"}`,
			wantField: "start_timecode",
		},
		{
			name:      "duplicate end with different values",
			body:      `{` + base + `,"end_timecode":"00:00:00;01"}`,
			wantField: "end_timecode",
		},
		{
			name:      "conflicting next_day authorization",
			body:      `{` + base + `,"next_day":false}`,
			wantField: "next_day",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := doConvert(t, c.body)
			assertError(t, status, body, "AMBIGUOUS_FIELD", c.wantField)
		})
	}
}

func TestDuplicateFieldWithSameValueAccepted(t *testing.T) {
	body := `{"direction":"timecode_span","rate":"30000/1001","start_timecode":"00:10:00;00",` +
		`"start_timecode":"00:10:00;00","end_timecode":"00:10:00;01"}`
	status, resp := doConvert(t, body)
	require.Equal(t, http.StatusOK, status, "body: %v", resp)
	assert.Equal(t, float64(1), resp["elapsed_frames"])
}

func TestMultipleJSONObjectsRejected(t *testing.T) {
	body := `{"direction":"timecode_span","rate":"30000/1001","start_timecode":"00:10:00;00",` +
		`"end_timecode":"00:10:00;01"}{"direction":"timecode_to_frame","rate":"30000/1001",` +
		`"timecode":"00:10:00;00"}`
	status, resp := doConvert(t, body)
	require.Equal(t, http.StatusBadRequest, status)
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %v", resp)
	assert.Equal(t, "MALFORMED_JSON", errObj["code"])
	assert.NotContains(t, resp, "elapsed_frames")
}

func TestMalformedJSON(t *testing.T) {
	status, body := doConvert(t, "{not json")
	require.Equal(t, http.StatusBadRequest, status)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %v", body)
	assert.Equal(t, "MALFORMED_JSON", errObj["code"])
}

func TestRoundTripOverAPI(t *testing.T) {
	for _, rate := range []string{"30000/1001", "60000/1001"} {
		// Sample every 10-minute block plus dropped-minute interiors.
		indices := []int64{0, 1, 1799, 1800, 3597, 17981, 17982}
		if rate == "60000/1001" {
			indices = []int64{0, 1, 3599, 3600, 7195, 35963, 35964}
		}
		for _, idx := range indices {
			status, body := doConvert(t, map[string]any{
				"direction": "frame_to_timecode", "rate": rate, "frame_index": idx,
			})
			require.Equal(t, http.StatusOK, status)
			tc, ok := body["timecode"].(string)
			require.True(t, ok)

			status2, body2 := doConvert(t, map[string]any{
				"direction": "timecode_to_frame", "rate": rate, "timecode": tc,
			})
			require.Equal(t, http.StatusOK, status2)
			assert.Equal(t, float64(idx), body2["frame_index"],
				"rate=%s round trip %d -> %s", rate, idx, tc)
		}
	}
}

// assertError checks the stable error envelope: 422, code, field, and no
// partial conversion values.
func assertError(t *testing.T, status int, body map[string]any, wantCode, wantField string) {
	t.Helper()
	require.Equal(t, http.StatusUnprocessableEntity, status, "body: %v", body)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %v", body)
	assert.Equal(t, wantCode, errObj["code"])
	assert.Equal(t, wantField, errObj["field"])
	assert.NotEmpty(t, errObj["message"])
	assert.NotContains(t, body, "frame_index", "error must not carry partial results")
	assert.NotContains(t, body, "timecode", "error must not carry partial results")
	assert.NotContains(t, body, "elapsed_frames", "error must not carry partial results")
}
