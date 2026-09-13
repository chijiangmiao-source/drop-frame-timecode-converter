package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"dropframe-api/internal/audit"
	"dropframe-api/internal/timecode"
)

type auditSegmentRequest struct {
	ID  string `json:"id"`
	In  string `json:"in"`
	Out string `json:"out"`
}

type createAuditRequest struct {
	Rate     string                `json:"rate"`
	Segments []auditSegmentRequest `json:"segments"`
}

// handleCreateAudit validates the submitted segment list, stores the audit
// report and returns it with its audit number. Invalid lists are rejected
// with the shared error envelope and nothing is stored.
func handleCreateAudit(repo *audit.Repository) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createAuditRequest
		if err := decodeStrictJSON(c.Request.Body, &req); err != nil {
			var ambiguous ambiguousFieldError
			if errors.As(err, &ambiguous) {
				respondError(c, http.StatusUnprocessableEntity, timecode.CodeAmbiguousField, ambiguous.Field(),
					"field appears multiple times with different values and its meaning is ambiguous")
				return
			}
			respondError(c, http.StatusBadRequest, "MALFORMED_JSON", "",
				"request body must be a single JSON object with fields rate and segments")
			return
		}

		rate, ok := timecode.ParseRate(req.Rate)
		if !ok {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeInvalidRate, "rate",
				"rate must be \"30000/1001\" or \"60000/1001\"")
			return
		}
		if req.Segments == nil {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "segments",
				"segments is required and must list at least two segments in broadcast order")
			return
		}

		segments := make([]audit.Segment, len(req.Segments))
		for i, s := range req.Segments {
			segments[i] = audit.Segment{ID: s.ID, In: s.In, Out: s.Out}
		}
		report, err := repo.Create(rate, segments)
		if err != nil {
			respondConversionError(c, err)
			return
		}
		c.JSON(http.StatusCreated, reportJSON(report))
	}
}

// handleGetAudit returns a previously created report by its audit number;
// unknown numbers get a stable 404 AUDIT_NOT_FOUND envelope.
func handleGetAudit(repo *audit.Repository) gin.HandlerFunc {
	return func(c *gin.Context) {
		report, err := repo.Get(c.Param("id"))
		if err != nil {
			if te, ok := err.(*timecode.Error); ok {
				respondError(c, http.StatusNotFound, te.Code, te.Field, te.Message)
				return
			}
			respondError(c, http.StatusInternalServerError, "INTERNAL", "", err.Error())
			return
		}
		c.JSON(http.StatusOK, reportJSON(report))
	}
}

// reportJSON renders a report in the same shape for creation and lookup.
func reportJSON(r audit.Report) gin.H {
	issues := make([]gin.H, len(r.Issues))
	for i, issue := range r.Issues {
		issues[i] = gin.H{
			"kind":             string(issue.Kind),
			"previous_segment": issue.PreviousID,
			"next_segment":     issue.NextID,
			"frames":           issue.Frames,
		}
	}
	return gin.H{
		"audit_id":      r.ID,
		"rate":          r.Rate,
		"status":        string(r.Status),
		"segment_count": r.SegmentCount,
		"issues":        issues,
	}
}
