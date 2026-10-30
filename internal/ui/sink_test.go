package ui

import "testing"

func TestConfirmRequestMarkers(t *testing.T) {
	req := ConfirmRequest{ToolName: "x", Summary: "y", HighRisk: true}
	if !req.HighRisk {
		t.Fatal("HighRisk flag lost")
	}
}
