package pipeline

import (
	"testing"
	"time"

	"booth-display/controller/internal/config"
)

func TestCanvasMixerTransitions(t *testing.T) {
	canvas := config.CanvasConfig{
		Width:  1920,
		Height: 540,
		FPS:    30,
	}
	displays := []config.DisplayConfig{
		{
			ID:     "disp1",
			Width:  1920,
			Height: 540,
			CropX:  0,
			CropY:  0,
		},
	}

	mixer := NewCanvasMixer(canvas, displays)
	if err := mixer.Start(); err != nil {
		t.Fatalf("failed to start mixer: %v", err)
	}
	defer mixer.Stop()

	// Initial status
	st := mixer.GetStatus()
	if st.ActiveSource != "testpattern" {
		t.Errorf("expected initial source 'testpattern', got %s", st.ActiveSource)
	}

	// Test Cut to testpattern
	if err := mixer.SwitchSource("testpattern", "", TransitionCut, 0); err != nil {
		t.Fatalf("SwitchSource cut failed: %v", err)
	}

	// Test Fade transition
	if err := mixer.SwitchSource("testpattern", "", TransitionFade, 100); err != nil {
		t.Fatalf("SwitchSource fade failed: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	stFade := mixer.GetStatus()
	if !stFade.InTransition {
		t.Logf("Transition completed quickly or in transition: %v", stFade.InTransition)
	}

	// Wait for transition to complete
	time.Sleep(120 * time.Millisecond)
	stEnd := mixer.GetStatus()
	if stEnd.InTransition {
		t.Errorf("expected transition to be finished after duration")
	}

	// Test Black transition
	if err := mixer.SwitchSource("testpattern", "", TransitionBlack, 100); err != nil {
		t.Fatalf("SwitchSource black failed: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
}
