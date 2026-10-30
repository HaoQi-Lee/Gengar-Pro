package safety

import "testing"

// TestClassifyCommand_HighRiskFromUser ensures user-flagged high-risk commands
// (reboot/shutdown/init 0|6/passwd) are classified as Risky and therefore
// gated behind a user confirmation prompt before execution.
func TestClassifyCommand_HighRiskFromUser(t *testing.T) {
	gate := NewGate()
	for _, cmd := range []string{"reboot now", "shutdown -h now", "init 0", "init 6", "passwd root"} {
		if level := gate.ClassifyCommand(cmd); level != LevelRisky {
			t.Errorf("ClassifyCommand(%q) = %v, want Risky", cmd, level)
		}
	}
}

func TestClassifyCommand_ProtectedPaths(t *testing.T) {
	for _, p := range protectedPaths {
		if p == "" {
			t.Error("protected path should not be empty")
		}
	}
}
