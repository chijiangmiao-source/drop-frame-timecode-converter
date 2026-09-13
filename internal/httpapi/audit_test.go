package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doAuditRequest issues one request against a fresh router (and therefore a
// fresh report repository) and returns status plus decoded body.
func doAuditRequest(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	var err error
	switch v := body.(type) {
	case nil:
	case string:
		raw = []byte(v)
	default:
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	NewRouter().ServeHTTP(w, req)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decoded), "response must be JSON: %q", w.Body.String())
	return w.Code, decoded
}

func createAudit(t *testing.T, body any) (int, map[string]any) {
	t.Helper()
	return doAuditRequest(t, http.MethodPost, "/api/v1/audits", body)
}

func contiguousSegments() []map[string]any {
	return []map[string]any{
		{"id": "seg-1", "in": "00:00:00;00", "out": "00:00:59;29"},
		{"id": "seg-2", "in": "00:01:00;02", "out": "00:09:59;29"},
		{"id": "seg-3", "in": "00:10:00;00", "out": "00:10:00;00"},
	}
}

func TestCreateAuditContiguousPasses(t *testing.T) {
	status, body := createAudit(t, map[string]any{
		"rate": "30000/1001", "segments": contiguousSegments(),
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	assert.NotEmpty(t, body["audit_id"])
	assert.Equal(t, "30000/1001", body["rate"])
	assert.Equal(t, "passed", body["status"])
	assert.Equal(t, float64(3), body["segment_count"])
	issues, ok := body["issues"].([]any)
	require.True(t, ok, "issues must be a JSON array: %v", body)
	assert.Empty(t, issues)
	assert.NotContains(t, body, "error")
}

func TestCreateAuditDetectsGapAndOverlap(t *testing.T) {
	status, body := createAudit(t, map[string]any{
		"rate": "30000/1001",
		"segments": []map[string]any{
			{"id": "seg-a", "in": "00:00:01;00", "out": "00:00:10;00"},
			{"id": "seg-b", "in": "00:00:12;00", "out": "00:00:20;00"},
			{"id": "seg-c", "in": "00:00:19;20", "out": "00:00:30;00"},
		},
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	assert.Equal(t, "failed", body["status"])
	issues, ok := body["issues"].([]any)
	require.True(t, ok)
	require.Len(t, issues, 2)

	gap, ok := issues[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gap", gap["kind"])
	assert.Equal(t, "seg-a", gap["previous_segment"])
	assert.Equal(t, "seg-b", gap["next_segment"])
	assert.Equal(t, float64(59), gap["frames"])

	overlap, ok := issues[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "overlap", overlap["kind"])
	assert.Equal(t, "seg-b", overlap["previous_segment"])
	assert.Equal(t, "seg-c", overlap["next_segment"])
	assert.Equal(t, float64(11), overlap["frames"])
}

func TestGetAuditReturnsCreatedReport(t *testing.T) {
	// Create and read back through the same router so the repository is shared.
	router := NewRouter()
	do := func(method, path string, payload any) (int, map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decoded))
		return w.Code, decoded
	}

	status, created := do(http.MethodPost, "/api/v1/audits", map[string]any{
		"rate": "60000/1001",
		"segments": []map[string]any{
			{"id": "one", "in": "00:00:00;00", "out": "00:00:10;00"},
			{"id": "two", "in": "00:00:10;02", "out": "00:00:20;00"},
		},
	})
	require.Equal(t, http.StatusCreated, status, "body: %v", created)
	id, ok := created["audit_id"].(string)
	require.True(t, ok && id != "", "create response must carry audit_id: %v", created)

	status, fetched := do(http.MethodGet, "/api/v1/audits/"+id, nil)
	require.Equal(t, http.StatusOK, status, "body: %v", fetched)
	assert.Equal(t, created, fetched, "lookup must return the same report as creation")
	assert.Equal(t, "failed", fetched["status"])
}

func TestGetAuditUnknownID(t *testing.T) {
	status, body := doAuditRequest(t, http.MethodGet, "/api/v1/audits/aud-999999", nil)
	require.Equal(t, http.StatusNotFound, status, "body: %v", body)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %v", body)
	assert.Equal(t, "AUDIT_NOT_FOUND", errObj["code"])
	assert.Equal(t, "audit_id", errObj["field"])
	assert.NotEmpty(t, errObj["message"])
	assert.NotContains(t, body, "audit_id")
	assert.NotContains(t, body, "issues")
}

func TestCreateAuditRejectedAndNotStored(t *testing.T) {
	cases := []struct {
		name      string
		body      any
		wantCode  string
		wantField string
	}{
		{"missing segments", map[string]any{
			"rate": "30000/1001"},
			"MISSING_FIELD", "segments"},
		{"fewer than two segments", map[string]any{
			"rate": "30000/1001",
			"segments": []map[string]any{
				{"id": "only", "in": "00:00:00;00", "out": "00:00:01;00"},
			}},
			"TOO_FEW_SEGMENTS", "segments"},
		{"duplicate identifier", map[string]any{
			"rate": "30000/1001",
			"segments": []map[string]any{
				{"id": "dup", "in": "00:00:00;00", "out": "00:00:01;00"},
				{"id": "dup", "in": "00:00:01;01", "out": "00:00:02;00"},
			}},
			"DUPLICATE_SEGMENT_ID", "segments[1].id"},
		{"out before in", map[string]any{
			"rate": "30000/1001",
			"segments": []map[string]any{
				{"id": "a", "in": "00:00:10;00", "out": "00:00:05;00"},
				{"id": "b", "in": "00:00:10;01", "out": "00:00:20;00"},
			}},
			"END_BEFORE_START", "segments[0].out"},
		{"illegal timecode", map[string]any{
			"rate": "30000/1001",
			"segments": []map[string]any{
				{"id": "a", "in": "00:00:00;00", "out": "00:00:10;00"},
				{"id": "b", "in": "00:01:00;01", "out": "00:02:00;00"},
			}},
			"DROPPED_FRAME_LABEL", "segments[1].in"},
		{"invalid rate", map[string]any{
			"rate": "25", "segments": contiguousSegments()},
			"INVALID_RATE", "rate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := createAudit(t, c.body)
			require.Equal(t, http.StatusUnprocessableEntity, status, "body: %v", body)
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok, "error envelope missing: %v", body)
			assert.Equal(t, c.wantCode, errObj["code"])
			assert.Equal(t, c.wantField, errObj["field"])
			assert.NotEmpty(t, errObj["message"])
			// A failed creation must not carry any audit result.
			assert.NotContains(t, body, "audit_id")
			assert.NotContains(t, body, "status")
			assert.NotContains(t, body, "issues")
		})
	}
}

func TestCreateAuditRejectedDoesNotConsumeAuditNumber(t *testing.T) {
	router := NewRouter()
	do := func(payload any) (int, map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audits", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decoded))
		return w.Code, decoded
	}

	status, _ := do(map[string]any{"rate": "30000/1001", "segments": []map[string]any{
		{"id": "only", "in": "00:00:00;00", "out": "00:00:01;00"},
	}})
	require.Equal(t, http.StatusUnprocessableEntity, status)

	status, body := do(map[string]any{"rate": "30000/1001", "segments": contiguousSegments()})
	require.Equal(t, http.StatusCreated, status, "body: %v", body)
	assert.Equal(t, "aud-000001", body["audit_id"],
		"a rejected creation must not consume an audit number")
}

func TestCreateAuditMalformedJSON(t *testing.T) {
	status, body := createAudit(t, "{not json")
	require.Equal(t, http.StatusBadRequest, status)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %v", body)
	assert.Equal(t, "MALFORMED_JSON", errObj["code"])
	assert.NotContains(t, body, "audit_id")
}

func TestCreateAuditAmbiguousFieldsRejected(t *testing.T) {
	base := `"rate":"30000/1001","segments":[{"id":"a","in":"00:00:00;00","out":"00:00:01;00"},` +
		`{"id":"b","in":"00:00:01;01","out":"00:00:02;00"}]`
	cases := []struct {
		name      string
		body      string
		wantField string
	}{
		{"duplicate rate with different values", `{` + base + `,"rate":"60000/1001"}`, "rate"},
		{"case-variant rate in conflict", `{"Rate":"60000/1001",` + base + `}`, "rate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := createAudit(t, c.body)
			require.Equal(t, http.StatusUnprocessableEntity, status, "body: %v", body)
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok, "error envelope missing: %v", body)
			assert.Equal(t, "AMBIGUOUS_FIELD", errObj["code"])
			assert.Equal(t, c.wantField, errObj["field"])
			assert.NotContains(t, body, "audit_id")
		})
	}
}

func TestAuditNumbersAreUniqueAcrossCreations(t *testing.T) {
	router := NewRouter()
	seen := make(map[string]bool)
	for i := 0; i < 3; i++ {
		raw, err := json.Marshal(map[string]any{"rate": "30000/1001", "segments": contiguousSegments()})
		require.NoError(t, err)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/audits", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, http.StatusCreated, w.Code, "body: %v", body)
		id, _ := body["audit_id"].(string)
		assert.Equal(t, fmt.Sprintf("aud-%06d", i+1), id)
		assert.False(t, seen[id], "audit id %q handed out twice", id)
		seen[id] = true
	}
}
