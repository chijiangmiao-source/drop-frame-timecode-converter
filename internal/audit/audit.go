// Package audit implements timeline audits for ingest crews: given a frame
// rate and the segment list of a timeline in broadcast order (identifier,
// in-point and out-point per segment), it validates every label with the
// existing drop-frame conversion rules, compares adjacent segments by real
// frame index and produces a persisted audit report. The report carries an
// audit number, a passed/failed status and an ordered issue list naming the
// previous and next segment identifiers plus the frame count of each gap or
// overlap, so downstream QC can read the result without redoing timecode
// arithmetic by hand.
package audit

import (
	"fmt"
	"sync"

	"dropframe-api/internal/timecode"
)

// Stable machine-readable error codes specific to timeline audits. They are
// timecode.Code values so the HTTP layer renders them in the same error
// envelope as the conversion endpoints.
const (
	CodeTooFewSegments     timecode.Code = "TOO_FEW_SEGMENTS"
	CodeDuplicateSegmentID timecode.Code = "DUPLICATE_SEGMENT_ID"
	CodeAuditNotFound      timecode.Code = "AUDIT_NOT_FOUND"
)

// Status is the outcome of an audit: passed when adjacent segments are
// contiguous, failed when at least one gap or overlap was found.
type Status string

const (
	StatusPassed Status = "passed"
	StatusFailed Status = "failed"
)

// IssueKind distinguishes the two discontinuities an audit can report.
type IssueKind string

const (
	IssueGap     IssueKind = "gap"
	IssueOverlap IssueKind = "overlap"
)

// Segment is one timeline entry in broadcast order. In and Out are
// HH:MM:SS;FF labels at the audit rate; both are inclusive, so a one-frame
// segment has identical in and out points.
type Segment struct {
	ID  string
	In  string
	Out string
}

// Issue describes one discontinuity between two adjacent segments. For a
// gap, Frames is the number of missing frames between the previous
// segment's out-point and the next segment's in-point. For an overlap,
// Frames is the number of frames both segments claim.
type Issue struct {
	Kind       IssueKind
	PreviousID string
	NextID     string
	Frames     int64
}

// Report is the persisted outcome of one audit.
type Report struct {
	ID           string
	Rate         string
	SegmentCount int
	Status       Status
	Issues       []Issue
}

// Check validates the segment list and compares adjacent segments by real
// frame index. Every label is fully validated (format, ranges, drop-frame
// legality) via the shared timecode rules, with failures reported against
// "segments[i].in" or "segments[i].out". The list is rejected as a whole —
// and no report may be stored — when it holds fewer than two segments, when
// an identifier is empty or duplicated, or when a segment's out-point falls
// before its in-point (END_BEFORE_START).
//
// On success it returns the audit status and the ordered issue list; a
// contiguous timeline yields StatusPassed and an empty (non-nil) slice.
func Check(rate timecode.Rate, segments []Segment) (Status, []Issue, error) {
	if len(segments) < 2 {
		return "", nil, &timecode.Error{
			Code:  CodeTooFewSegments,
			Field: "segments",
			Message: fmt.Sprintf("segments must list at least two segments in broadcast order, got %d",
				len(segments)),
		}
	}

	type resolvedSegment struct {
		id      string
		in, out int64
	}
	resolved := make([]resolvedSegment, len(segments))
	seen := make(map[string]int, len(segments))
	for i, seg := range segments {
		field := func(name string) string { return fmt.Sprintf("segments[%d].%s", i, name) }
		if seg.ID == "" {
			return "", nil, &timecode.Error{
				Code:    timecode.CodeMissingField,
				Field:   field("id"),
				Message: fmt.Sprintf("segments[%d] must carry a non-empty identifier", i),
			}
		}
		if first, dup := seen[seg.ID]; dup {
			return "", nil, &timecode.Error{
				Code:  CodeDuplicateSegmentID,
				Field: field("id"),
				Message: fmt.Sprintf("segment identifier %q is used by both segments[%d] and segments[%d]; identifiers must be unique",
					seg.ID, first, i),
			}
		}
		seen[seg.ID] = i

		if seg.In == "" {
			return "", nil, &timecode.Error{
				Code:    timecode.CodeMissingField,
				Field:   field("in"),
				Message: fmt.Sprintf("segments[%d] (id %q) must carry an in-point timecode", i, seg.ID),
			}
		}
		if seg.Out == "" {
			return "", nil, &timecode.Error{
				Code:    timecode.CodeMissingField,
				Field:   field("out"),
				Message: fmt.Sprintf("segments[%d] (id %q) must carry an out-point timecode", i, seg.ID),
			}
		}
		inIndex, err := frameIndexForField(rate, field("in"), seg.In)
		if err != nil {
			return "", nil, err
		}
		outIndex, err := frameIndexForField(rate, field("out"), seg.Out)
		if err != nil {
			return "", nil, err
		}
		if outIndex < inIndex {
			return "", nil, &timecode.Error{
				Code:  timecode.CodeEndBeforeStart,
				Field: field("out"),
				Message: fmt.Sprintf("segment %q has out-point %q before its in-point %q",
					seg.ID, seg.Out, seg.In),
			}
		}
		resolved[i] = resolvedSegment{id: seg.ID, in: inIndex, out: outIndex}
	}

	issues := make([]Issue, 0)
	for i := 1; i < len(resolved); i++ {
		prev, next := resolved[i-1], resolved[i]
		switch diff := next.in - prev.out; {
		case diff == 1:
			// Contiguous: the next segment starts on the frame right after
			// the previous segment's out-point.
		case diff > 1:
			issues = append(issues, Issue{
				Kind: IssueGap, PreviousID: prev.id, NextID: next.id, Frames: diff - 1,
			})
		default:
			issues = append(issues, Issue{
				Kind: IssueOverlap, PreviousID: prev.id, NextID: next.id, Frames: 1 - diff,
			})
		}
	}

	status := StatusPassed
	if len(issues) > 0 {
		status = StatusFailed
	}
	return status, issues, nil
}

// frameIndexForField validates a timecode label like
// timecode.FrameIndexFromTimecode but reports errors against the given
// request field name.
func frameIndexForField(rate timecode.Rate, field, tc string) (int64, error) {
	index, err := timecode.FrameIndexFromTimecode(rate, tc)
	if err != nil {
		if te, ok := err.(*timecode.Error); ok {
			return 0, &timecode.Error{Code: te.Code, Field: field, Message: te.Message}
		}
		return 0, err
	}
	return index, nil
}

// Repository stores audit reports in process memory and owns their
// create-then-read lifecycle: a report is created only after the segment
// list passes validation, and can then be read back by its audit number for
// as long as the process lives.
type Repository struct {
	mu      sync.RWMutex
	seq     uint64
	reports map[string]Report
}

// NewRepository returns an empty in-process report repository.
func NewRepository() *Repository {
	return &Repository{reports: make(map[string]Report)}
}

// Create audits the segment list at the given rate and, only when the list
// is valid, stores and returns the new report with its assigned audit
// number. Invalid lists are rejected with a *timecode.Error and nothing is
// stored.
func (r *Repository) Create(rate timecode.Rate, segments []Segment) (Report, error) {
	status, issues, err := Check(rate, segments)
	if err != nil {
		return Report{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	report := Report{
		ID:           fmt.Sprintf("aud-%06d", r.seq),
		Rate:         rate.ID(),
		SegmentCount: len(segments),
		Status:       status,
		Issues:       issues,
	}
	r.reports[report.ID] = report
	return report, nil
}

// Get returns the stored report with the given audit number. Unknown
// numbers are rejected with a stable AUDIT_NOT_FOUND error on field
// "audit_id".
func (r *Repository) Get(id string) (Report, error) {
	r.mu.RLock()
	report, ok := r.reports[id]
	r.mu.RUnlock()
	if !ok {
		return Report{}, &timecode.Error{
			Code:    CodeAuditNotFound,
			Field:   "audit_id",
			Message: fmt.Sprintf("audit report %q does not exist", id),
		}
	}
	return report, nil
}
