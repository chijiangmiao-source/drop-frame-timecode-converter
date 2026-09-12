// Package httpapi exposes the drop-frame timecode conversion over HTTP.
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"dropframe-api/internal/timecode"
)

// Directions accepted by the convert endpoint.
const (
	DirectionTimecodeToFrame = "timecode_to_frame"
	DirectionFrameToTimecode = "frame_to_timecode"
	DirectionTimecodeSpan    = "timecode_span"
)

type convertRequest struct {
	Direction     string  `json:"direction"`
	Rate          string  `json:"rate"`
	Timecode      *string `json:"timecode"`
	FrameIndex    *int64  `json:"frame_index"`
	StartTimecode *string `json:"start_timecode"`
	EndTimecode   *string `json:"end_timecode"`
	NextDay       bool    `json:"next_day"`
}

type errorBody struct {
	Code    timecode.Code `json:"code"`
	Field   string        `json:"field"`
	Message string        `json:"message"`
}

// respondError renders a stable error envelope and never includes partial
// conversion results.
func respondError(c *gin.Context, status int, code timecode.Code, field, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": errorBody{Code: code, Field: field, Message: message},
	})
}

func respondConversionError(c *gin.Context, err error) {
	if te, ok := err.(*timecode.Error); ok {
		respondError(c, http.StatusUnprocessableEntity, te.Code, te.Field, te.Message)
		return
	}
	respondError(c, http.StatusInternalServerError, "INTERNAL", "", err.Error())
}

// NewRouter builds the Gin engine with all routes registered.
func NewRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.POST("/api/v1/convert", handleConvert)
	return r
}

func handleConvert(c *gin.Context) {
	var req convertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "MALFORMED_JSON", "",
			"request body must be a JSON object with fields direction, rate and the inputs required by the chosen direction")
		return
	}

	if req.Direction != DirectionTimecodeToFrame && req.Direction != DirectionFrameToTimecode &&
		req.Direction != DirectionTimecodeSpan {
		respondError(c, http.StatusUnprocessableEntity, timecode.CodeInvalidDirection, "direction",
			"direction must be \"timecode_to_frame\", \"frame_to_timecode\" or \"timecode_span\"")
		return
	}

	rate, ok := timecode.ParseRate(req.Rate)
	if !ok {
		respondError(c, http.StatusUnprocessableEntity, timecode.CodeInvalidRate, "rate",
			"rate must be \"30000/1001\" or \"60000/1001\"")
		return
	}

	switch req.Direction {
	case DirectionTimecodeToFrame:
		if req.Timecode == nil {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "timecode",
				"timecode is required when direction is \"timecode_to_frame\"")
			return
		}
		index, err := timecode.FrameIndexFromTimecode(rate, *req.Timecode)
		if err != nil {
			respondConversionError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"frame_index": index})

	case DirectionFrameToTimecode:
		if req.FrameIndex == nil {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "frame_index",
				"frame_index is required when direction is \"frame_to_timecode\"")
			return
		}
		tc, err := timecode.TimecodeFromFrameIndex(rate, *req.FrameIndex)
		if err != nil {
			respondConversionError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"timecode": tc})

	case DirectionTimecodeSpan:
		if req.StartTimecode == nil {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "start_timecode",
				"start_timecode is required when direction is \"timecode_span\"")
			return
		}
		if req.EndTimecode == nil {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "end_timecode",
				"end_timecode is required when direction is \"timecode_span\"")
			return
		}
		elapsed, err := timecode.SpanFrames(rate, *req.StartTimecode, *req.EndTimecode, req.NextDay)
		if err != nil {
			respondConversionError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"elapsed_frames": elapsed})
	}
}
