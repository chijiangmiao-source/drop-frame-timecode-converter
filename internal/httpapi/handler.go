// Package httpapi exposes the drop-frame timecode conversion over HTTP.
package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"dropframe-api/internal/timecode"
)

// Directions accepted by the convert endpoint.
const (
	DirectionTimecodeToFrame = "timecode_to_frame"
	DirectionFrameToTimecode = "frame_to_timecode"
	DirectionTimecodeSpan    = "timecode_span"
	DirectionTimecodeOffset  = "timecode_offset"
	DirectionTimecodeRetime  = "timecode_retime"
)

type convertRequest struct {
	Direction     string  `json:"direction"`
	Rate          string  `json:"rate"`
	SourceRate    string  `json:"source_rate"`
	TargetRate    string  `json:"target_rate"`
	Timecode      *string `json:"timecode"`
	FrameIndex    *int64  `json:"frame_index"`
	FrameOffset   *int64  `json:"frame_offset"`
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
	if err := decodeStrictJSON(c.Request.Body, &req); err != nil {
		var ambiguous ambiguousFieldError
		if errors.As(err, &ambiguous) {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeAmbiguousField, ambiguous.Field(),
				"field appears multiple times with different values and its meaning is ambiguous")
			return
		}
		respondError(c, http.StatusBadRequest, "MALFORMED_JSON", "",
			"request body must be a single JSON object with fields direction, rate and the inputs required by the chosen direction")
		return
	}

	if req.Direction != DirectionTimecodeToFrame && req.Direction != DirectionFrameToTimecode &&
		req.Direction != DirectionTimecodeSpan && req.Direction != DirectionTimecodeOffset &&
		req.Direction != DirectionTimecodeRetime {
		respondError(c, http.StatusUnprocessableEntity, timecode.CodeInvalidDirection, "direction",
			"direction must be \"timecode_to_frame\", \"frame_to_timecode\", \"timecode_span\", \"timecode_offset\" or \"timecode_retime\"")
		return
	}

	if req.Direction == DirectionTimecodeRetime {
		handleTimecodeRetime(c, req)
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

	case DirectionTimecodeOffset:
		if req.Timecode == nil {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "timecode",
				"timecode is required when direction is \"timecode_offset\"")
			return
		}
		if req.FrameOffset == nil {
			respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "frame_offset",
				"frame_offset is required when direction is \"timecode_offset\"")
			return
		}
		result, err := timecode.OffsetTimecode(rate, *req.Timecode, *req.FrameOffset)
		if err != nil {
			respondConversionError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"timecode": result.Timecode, "day_offset": result.DayOffset})
	}
}

// handleTimecodeRetime migrates a locate point between the two supported
// rates: it takes source_rate, target_rate and timecode instead of rate.
func handleTimecodeRetime(c *gin.Context, req convertRequest) {
	source, ok := timecode.ParseRate(req.SourceRate)
	if !ok {
		respondError(c, http.StatusUnprocessableEntity, timecode.CodeInvalidRate, "source_rate",
			"source_rate must be \"30000/1001\" or \"60000/1001\"")
		return
	}
	target, ok := timecode.ParseRate(req.TargetRate)
	if !ok {
		respondError(c, http.StatusUnprocessableEntity, timecode.CodeInvalidRate, "target_rate",
			"target_rate must be \"30000/1001\" or \"60000/1001\"")
		return
	}
	if req.Timecode == nil {
		respondError(c, http.StatusUnprocessableEntity, timecode.CodeMissingField, "timecode",
			"timecode is required when direction is \"timecode_retime\"")
		return
	}
	tc, err := timecode.RetimeTimecode(source, target, *req.Timecode)
	if err != nil {
		respondConversionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"timecode": tc})
}
