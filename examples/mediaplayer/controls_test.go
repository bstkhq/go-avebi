package mediaplayer

import (
	"testing"
	"time"
)

func TestControlAtButtons(t *testing.T) {
	tests := []struct {
		name     string
		showOpen bool
		actions  []playbackAction
		deltas   []time.Duration
	}{
		{
			name:     "with picker",
			showOpen: true,
			actions:  []playbackAction{actionOpen, actionPlayPause, actionStop, actionSeekRelative, actionSeekRelative, actionToggleLoop},
			deltas:   []time.Duration{0, 0, 0, -5 * time.Second, 5 * time.Second, 0},
		},
		{
			name:    "without picker",
			actions: []playbackAction{actionPlayPause, actionStop, actionSeekRelative, actionSeekRelative, actionToggleLoop},
			deltas:  []time.Duration{0, 0, -5 * time.Second, 5 * time.Second, 0},
		},
	}

	for _, size := range []struct {
		name          string
		width, height int
	}{
		{name: "landscape", width: 640, height: 360},
		{name: "portrait", width: 360, height: 640},
	} {
		for _, test := range tests {
			t.Run(size.name+"/"+test.name, func(t *testing.T) {
				layout := newControlLayout(size.width, size.height, test.showOpen)
				if layout.buttonCount != len(test.actions) {
					t.Fatalf("button count = %d, want %d", layout.buttonCount, len(test.actions))
				}
				for index, wantAction := range test.actions {
					button := layout.buttons[index]
					input := controlAt(layout, button.x+button.width/2, button.y+button.height/2)
					if input.action != wantAction {
						t.Fatalf("button %d action = %v, want %v", index, input.action, wantAction)
					}
					if input.seekDelta != test.deltas[index] {
						t.Fatalf("button %d seek delta = %s, want %s", index, input.seekDelta, test.deltas[index])
					}
				}
			})
		}
	}
}

func TestLayoutWithoutPickerDoesNotExposeOpen(t *testing.T) {
	layout := newControlLayout(640, 360, false)
	for _, button := range layout.buttons[:layout.buttonCount] {
		input := controlAt(layout, button.x+button.width/2, button.y+button.height/2)
		if input.action == actionOpen {
			t.Fatal("layout without a picker exposed the Open action")
		}
	}
}

func TestControlAtTimeline(t *testing.T) {
	layout := newControlLayout(360, 640, true)
	tests := []struct {
		name string
		x    int
		want float64
	}{
		{name: "start", x: layout.timeline.x, want: 0},
		{name: "middle", x: layout.timeline.x + layout.timeline.width/2, want: 0.5},
		{name: "end", x: layout.timeline.x + layout.timeline.width, want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := controlAt(layout, test.x, layout.timeline.y+layout.timeline.height/2)
			if input.action != actionSeekAbsolute {
				t.Fatalf("action = %v, want %v", input.action, actionSeekAbsolute)
			}
			if input.seekRatio != test.want {
				t.Fatalf("seek ratio = %v, want %v", input.seekRatio, test.want)
			}
		})
	}
}

func TestControlAtOutsideControls(t *testing.T) {
	layout := newControlLayout(640, 360, false)
	if input := controlAt(layout, layout.width-1, layout.height-1); input.action != actionNone {
		t.Fatalf("action = %v, want no action", input.action)
	}
}

func TestControlAtButtonTopEdge(t *testing.T) {
	layout := newControlLayout(640, 360, true)
	button := layout.buttons[0]
	input := controlAt(layout, button.x+button.width/2, button.y)
	if input.action != actionOpen {
		t.Fatalf("action = %v, want %v", input.action, actionOpen)
	}
}

func TestLogicalSizePreservesOrientationAndAspectRatio(t *testing.T) {
	tests := []struct {
		name                        string
		outsideWidth, outsideHeight int
		wantWidth, wantHeight       int
	}{
		{name: "default", wantWidth: 640, wantHeight: 360},
		{name: "landscape tablet", outsideWidth: 2560, outsideHeight: 1600, wantWidth: 576, wantHeight: 360},
		{name: "portrait tablet", outsideWidth: 1600, outsideHeight: 2560, wantWidth: 360, wantHeight: 576},
		{name: "wide phone", outsideWidth: 2400, outsideHeight: 1080, wantWidth: 800, wantHeight: 360},
		{name: "tall phone", outsideWidth: 1080, outsideHeight: 2400, wantWidth: 360, wantHeight: 800},
		{name: "square", outsideWidth: 1000, outsideHeight: 1000, wantWidth: 360, wantHeight: 360},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			width, height := logicalSize(test.outsideWidth, test.outsideHeight)
			if width != test.wantWidth || height != test.wantHeight {
				t.Fatalf("logical size = %dx%d, want %dx%d", width, height, test.wantWidth, test.wantHeight)
			}
		})
	}
}

func TestControlLayoutAdaptsToWidth(t *testing.T) {
	for _, showOpen := range []bool{false, true} {
		for _, test := range []struct {
			name          string
			width, height int
			stacked       bool
		}{
			{name: "landscape", width: 640, height: 360},
			{name: "portrait", width: 360, height: 640, stacked: true},
			{name: "square", width: 360, height: 360, stacked: true},
			{name: "ultrawide", width: 800, height: 360},
		} {
			t.Run(fmtBool(showOpen)+"/"+test.name, func(t *testing.T) {
				layout := newControlLayout(test.width, test.height, showOpen)
				if layout.stacked != test.stacked {
					t.Fatalf("stacked = %v, want %v", layout.stacked, test.stacked)
				}
				if layout.panelY < 20 || layout.timeline.x < 0 || layout.timeline.x+layout.timeline.width > layout.width {
					t.Fatalf("invalid panel or timeline geometry: %#v", layout)
				}
				for i, button := range layout.buttons[:layout.buttonCount] {
					if button.x < 0 || button.y < layout.panelY || button.x+button.width > layout.width || button.y+button.height > layout.height {
						t.Fatalf("button %d outside layout: %#v in %#v", i, button, layout)
					}
				}
			})
		}
	}
}

func fmtBool(value bool) string {
	if value {
		return "with picker"
	}
	return "without picker"
}

func TestPlaybackProgress(t *testing.T) {
	tests := []struct {
		name     string
		position time.Duration
		duration time.Duration
		want     float64
	}{
		{name: "unknown duration", position: time.Second, want: 0},
		{name: "before start", position: -time.Second, duration: 10 * time.Second, want: 0},
		{name: "middle", position: 5 * time.Second, duration: 10 * time.Second, want: 0.5},
		{name: "past end", position: 11 * time.Second, duration: 10 * time.Second, want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := playbackProgress(test.position, test.duration); got != test.want {
				t.Fatalf("progress = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFormatDiagnostics(t *testing.T) {
	if got, want := formatDiagnostics(59.94, 60, 1920, 1080, "h264"), "TPS 59.9 | FPS 60.0 | Video 1920x1080 | Codec h264"; got != want {
		t.Fatalf("diagnostics = %q, want %q", got, want)
	}
	if got, want := formatDiagnostics(0, 0, 0, 0, ""), "TPS 0.0 | FPS 0.0 | Video -- | Codec --"; got != want {
		t.Fatalf("empty diagnostics = %q, want %q", got, want)
	}
}
