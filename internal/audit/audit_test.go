package audit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dropframe-api/internal/timecode"
)

func TestCheckContiguousTimelinePasses(t *testing.T) {
	// The second segment starts on the frame right after the first segment's
	// out-point, including across a dropped-minute boundary where the labels
	// jump (00:00:59;29 -> 00:01:00;02) while the frame indices stay
	// continuous.
	status, issues, err := Check(timecode.Rate2997, []Segment{
		{ID: "seg-a", In: "00:00:00;00", Out: "00:00:59;29"},
		{ID: "seg-b", In: "00:01:00;02", Out: "00:09:59;29"},
		{ID: "seg-c", In: "00:10:00;00", Out: "00:10:00;00"},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusPassed, status)
	assert.NotNil(t, issues)
	assert.Empty(t, issues)
}

func TestCheckDetectsGapAndOverlapInOrder(t *testing.T) {
	// seg-a ends at frame 300 (00:00:10;00), seg-b starts at frame 360
	// (00:00:12;00): a 59-frame gap. seg-c starts at frame 590
	// (00:00:19;20) while seg-b ends at frame 600 (00:00:20;00): an
	// 11-frame overlap.
	status, issues, err := Check(timecode.Rate2997, []Segment{
		{ID: "seg-a", In: "00:00:01;00", Out: "00:00:10;00"},
		{ID: "seg-b", In: "00:00:12;00", Out: "00:00:20;00"},
		{ID: "seg-c", In: "00:00:19;20", Out: "00:00:30;00"},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, status)
	require.Len(t, issues, 2)
	assert.Equal(t, Issue{Kind: IssueGap, PreviousID: "seg-a", NextID: "seg-b", Frames: 59}, issues[0])
	assert.Equal(t, Issue{Kind: IssueOverlap, PreviousID: "seg-b", NextID: "seg-c", Frames: 11}, issues[1])
}

func TestCheckDetectsGapAtRate60(t *testing.T) {
	// 00:00:10;00 is frame 600 at 60000/1001; 00:00:10;02 is frame 602.
	status, issues, err := Check(timecode.Rate5994, []Segment{
		{ID: "first", In: "00:00:00;00", Out: "00:00:10;00"},
		{ID: "second", In: "00:00:10;02", Out: "00:00:20;00"},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, status)
	require.Len(t, issues, 1)
	assert.Equal(t, Issue{Kind: IssueGap, PreviousID: "first", NextID: "second", Frames: 1}, issues[0])
}

func TestCheckRejectsInvalidLists(t *testing.T) {
	cases := []struct {
		name      string
		rate      timecode.Rate
		segments  []Segment
		wantCode  timecode.Code
		wantField string
	}{
		{"no segments", timecode.Rate2997, nil, CodeTooFewSegments, "segments"},
		{"single segment", timecode.Rate2997, []Segment{
			{ID: "only", In: "00:00:00;00", Out: "00:00:01;00"},
		}, CodeTooFewSegments, "segments"},
		{"duplicate identifier", timecode.Rate2997, []Segment{
			{ID: "dup", In: "00:00:00;00", Out: "00:00:01;00"},
			{ID: "dup", In: "00:00:01;01", Out: "00:00:02;00"},
		}, CodeDuplicateSegmentID, "segments[1].id"},
		{"empty identifier", timecode.Rate2997, []Segment{
			{ID: "", In: "00:00:00;00", Out: "00:00:01;00"},
			{ID: "b", In: "00:00:01;01", Out: "00:00:02;00"},
		}, timecode.CodeMissingField, "segments[0].id"},
		{"missing in-point", timecode.Rate2997, []Segment{
			{ID: "a", Out: "00:00:01;00"},
			{ID: "b", In: "00:00:01;01", Out: "00:00:02;00"},
		}, timecode.CodeMissingField, "segments[0].in"},
		{"missing out-point", timecode.Rate2997, []Segment{
			{ID: "a", In: "00:00:00;00"},
			{ID: "b", In: "00:00:01;01", Out: "00:00:02;00"},
		}, timecode.CodeMissingField, "segments[0].out"},
		{"out before in", timecode.Rate2997, []Segment{
			{ID: "a", In: "00:00:10;00", Out: "00:00:05;00"},
			{ID: "b", In: "00:00:10;01", Out: "00:00:20;00"},
		}, timecode.CodeEndBeforeStart, "segments[0].out"},
		{"bad in-point format", timecode.Rate2997, []Segment{
			{ID: "a", In: "00:00:00:00", Out: "00:00:01;00"},
			{ID: "b", In: "00:00:01;01", Out: "00:00:02;00"},
		}, timecode.CodeInvalidTimecodeFormat, "segments[0].in"},
		{"dropped in-point label", timecode.Rate2997, []Segment{
			{ID: "a", In: "00:00:00;00", Out: "00:00:10;00"},
			{ID: "b", In: "00:01:00;01", Out: "00:02:00;00"},
		}, timecode.CodeDroppedFrameLabel, "segments[1].in"},
		{"dropped out-point label at rate 60", timecode.Rate5994, []Segment{
			{ID: "a", In: "00:00:00;00", Out: "00:01:00;03"},
			{ID: "b", In: "00:01:00;04", Out: "00:02:00;00"},
		}, timecode.CodeDroppedFrameLabel, "segments[0].out"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Check(c.rate, c.segments)
			require.Error(t, err)
			te, ok := err.(*timecode.Error)
			require.True(t, ok, "error must be a *timecode.Error, got %T", err)
			assert.Equal(t, c.wantCode, te.Code)
			assert.Equal(t, c.wantField, te.Field)
		})
	}
}

func TestRepositoryCreateThenGet(t *testing.T) {
	repo := NewRepository()
	segments := []Segment{
		{ID: "seg-a", In: "00:00:00;00", Out: "00:00:09;29"},
		{ID: "seg-b", In: "00:00:10;00", Out: "00:00:20;00"},
	}
	report, err := repo.Create(timecode.Rate2997, segments)
	require.NoError(t, err)
	assert.Equal(t, "aud-000001", report.ID)
	assert.Equal(t, "30000/1001", report.Rate)
	assert.Equal(t, 2, report.SegmentCount)
	assert.Equal(t, StatusPassed, report.Status)
	assert.Empty(t, report.Issues)

	loaded, err := repo.Get(report.ID)
	require.NoError(t, err)
	assert.Equal(t, report, loaded)

	second, err := repo.Create(timecode.Rate2997, segments)
	require.NoError(t, err)
	assert.Equal(t, "aud-000002", second.ID, "audit numbers must increase monotonically")
}

func TestRepositoryRejectedListIsNotStored(t *testing.T) {
	repo := NewRepository()
	_, err := repo.Create(timecode.Rate2997, []Segment{
		{ID: "only", In: "00:00:00;00", Out: "00:00:01;00"},
	})
	require.Error(t, err)

	// The failed creation must not consume an audit number.
	report, err := repo.Create(timecode.Rate2997, []Segment{
		{ID: "a", In: "00:00:00;00", Out: "00:00:01;00"},
		{ID: "b", In: "00:00:01;01", Out: "00:00:02;00"},
	})
	require.NoError(t, err)
	assert.Equal(t, "aud-000001", report.ID)
}

func TestRepositoryGetUnknownID(t *testing.T) {
	repo := NewRepository()
	_, err := repo.Get("aud-000042")
	require.Error(t, err)
	te, ok := err.(*timecode.Error)
	require.True(t, ok)
	assert.Equal(t, CodeAuditNotFound, te.Code)
	assert.Equal(t, "audit_id", te.Field)
}
