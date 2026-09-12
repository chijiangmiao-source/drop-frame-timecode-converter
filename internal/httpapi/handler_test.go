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
}
