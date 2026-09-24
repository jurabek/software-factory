package reviewer

import "testing"

func TestValidateAcceptsApprovedReview(t *testing.T) {
	payload := `{"status":"success","summary":"Looks good","notes_for_next_agent":"","report_markdown":"# Review\n\nLooks good.","approved":true,"findings":[],"blocking":[]}`
	if _, err := Validate(payload); err != nil {
		t.Fatalf("valid approved review rejected: %v", err)
	}
}

func TestValidateAcceptsRejectedReviewWithBlocking(t *testing.T) {
	payload := `{"status":"success","summary":"Needs work","notes_for_next_agent":"","report_markdown":"# Review","approved":false,"findings":[{"requirement":"tests","met":false,"evidence":"missing"}],"blocking":["add tests"]}`
	if _, err := Validate(payload); err != nil {
		t.Fatalf("valid rejected review rejected: %v", err)
	}
}

func TestValidateRejectsApprovedReviewWithUnmetFinding(t *testing.T) {
	payload := `{"status":"success","summary":"Looks good","notes_for_next_agent":"","report_markdown":"# Review","approved":true,"findings":[{"requirement":"tests","met":false,"evidence":"missing"}],"blocking":[]}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("approved review with unmet finding accepted")
	}
}

func TestValidateRejectsApprovedReviewWithBlocking(t *testing.T) {
	payload := `{"status":"success","summary":"Looks good","notes_for_next_agent":"","report_markdown":"# Review","approved":true,"findings":[],"blocking":["fix"]}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("approved review with blocking findings accepted")
	}
}

func TestValidateRejectsRejectionWithoutBlocking(t *testing.T) {
	payload := `{"status":"success","summary":"Needs work","notes_for_next_agent":"","report_markdown":"# Review","approved":false,"findings":[],"blocking":[]}`
	if _, err := Validate(payload); err == nil {
		t.Fatal("rejected review without blocking findings accepted")
	}
}
