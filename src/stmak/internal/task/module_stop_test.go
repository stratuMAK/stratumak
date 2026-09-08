package task

import (
	"log/slog"
	"os"
	"testing"
)

// TestModuleStopDisablesMotion pins the last thing Stop does. Stop runs ahead of
// the realtime barrier, so the disable still reaches the drives on the servo
// cycles that follow — and joints left enabled past it are joints motion keeps
// policing while the fieldbus is being taken down, which ends a clean shutdown
// with "joint N following error".
func TestModuleStopDisablesMotion(t *testing.T) {
	task, mot, _ := newTestTask()
	m := &milltaskModule{
		task:   task,
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	m.Stop()

	if !mot.hasCall("Disable") {
		t.Errorf("Stop did not disable motion; calls = %v", mot.calls)
	}
	if !mot.hasCall("Abort") {
		t.Errorf("Stop did not abort motion; calls = %v", mot.calls)
	}
}
