package mediaplayer

import (
	"testing"
	"time"
)

func TestQueueCloseCancelsPendingOperations(t *testing.T) {
	Game := New(Options{})
	Game.QueueOpen("media.mp4", false)
	Game.QueueSeek(5 * time.Second)
	Game.QueueClose()

	Game.mutex.Lock()
	defer Game.mutex.Unlock()
	if Game.pendingOpen.set {
		t.Fatal("pending open was not cancelled")
	}
	if Game.seekPending {
		t.Fatal("pending seek was not cancelled")
	}
	if !Game.closePending {
		t.Fatal("close was not queued")
	}
}

func TestQueueOpenPreservesPickerFileOwnership(t *testing.T) {
	Game := New(Options{})
	Game.QueueOpen("picker-cache.mp4", true)
	Game.QueueOpen("picker-cache.mp4", false)

	Game.mutex.Lock()
	defer Game.mutex.Unlock()
	if !Game.pendingOpen.owned {
		t.Fatal("picker-owned path lost its cleanup responsibility")
	}
}

func TestFilePickerControlsOpenVisibility(t *testing.T) {
	Game := New(Options{})
	if layout := newControlLayout(640, 360, Game.filePicker != nil); layout.buttonCount != 5 {
		t.Fatalf("buttons without picker = %d, want 5", layout.buttonCount)
	}

	Game.SetFilePicker(func() {})
	Game.mutex.Lock()
	showOpen := Game.filePicker != nil
	Game.mutex.Unlock()
	if layout := newControlLayout(640, 360, showOpen); layout.buttonCount != 6 {
		t.Fatalf("buttons with picker = %d, want 6", layout.buttonCount)
	}
}
