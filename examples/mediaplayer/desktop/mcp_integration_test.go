//go:build !ios && !android && (amd64 || arm64)

package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ebitenmcp "github.com/bstkhq/go-ebiten-mcp"
	"github.com/erparts/go-avebi"
	mediaplayer "github.com/erparts/go-avebi/examples/mediaplayer"
	"github.com/hajimehoshi/ebiten/v2"
)

const mcpTestMediaEnv = "AVEBI_MCP_TEST_MEDIA"

var (
	mcpTestMedia    string
	mcpFactoryCalls atomic.Int32
)

func TestMain(m *testing.M) {
	mcpTestMedia = os.Getenv(mcpTestMediaEnv)
	if mcpTestMedia == "" {
		os.Exit(m.Run())
	}
	if err := avebi.CreateAudioContextForMedia(mcpTestMedia); err != nil {
		fmt.Fprintf(os.Stderr, "mediaplayer MCP tests need media with audio: %v\n", err)
		os.Exit(2)
	}

	ebitenmcp.RunTests(
		m,
		func() ebiten.Game {
			// RunTests needs an initial game before tests begin. Keep that first
			// instance resource-free; ebitenmcp.T replaces it with the real one.
			if mcpFactoryCalls.Add(1) == 1 {
				return mediaplayer.New(mediaplayer.Options{})
			}
			game := mediaplayer.New(mediaplayer.Options{})
			_ = game.Open(mcpTestMedia)
			return game
		},
		ebitenmcp.WithName("avebi-ffmpeg-torture"),
		ebitenmcp.WithCaptureStage(ebitenmcp.StageOffscreen),
		ebitenmcp.WithState("player", func(current ebiten.Game) any {
			return current.(*mediaplayer.Game).Snapshot()
		}),
	)
}

type mcpTortureSnapshot = mediaplayer.Snapshot

func TestFFmpegMCPIntegrationAndTorture(t *testing.T) {
	if os.Getenv(mcpTestMediaEnv) == "" {
		t.Skip("set AVEBI_MCP_TEST_MEDIA to an H.264/AAC file")
	}

	driver := ebitenmcp.T(t)
	driver.Timeout = 30 * time.Second
	t.Cleanup(func() {
		driver.WithGame(func(current ebiten.Game) {
			if err := current.(*mediaplayer.Game).Close(); err != nil {
				t.Errorf("close torture player: %v", err)
			}
		})
	})

	initial := mcpSnapshot(t, driver)
	if !initial.HasAudio {
		t.Fatal("torture media did not select the audio controller")
	}
	if initial.Duration <= 0 {
		t.Fatalf("invalid media duration: %s", initial.Duration)
	}

	t.Log("phase: integration controls and captures")
	runMCPControlIntegration(t, driver)

	t.Log("phase: repeated play/pause/seek/stop torture")
	runMCPTorture(t, driver)

	if crash := driver.Runtime().Crash(); crash != nil {
		t.Fatalf("game crashed in %s at tick %d: %v\n%s", crash.Phase, crash.Tick, crash.Value, crash.Stack)
	}
}

func runMCPControlIntegration(t *testing.T, driver *ebitenmcp.Driver) {
	t.Helper()

	// The example intentionally autoplays. Restart it here so short CI fixtures
	// cannot reach EOF while go-ebiten-mcp is attaching to the game.
	driver.Tap(ebiten.KeyS)
	driver.Tap(ebiten.KeySpace)
	waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.State == avebi.Playing && s.FramePTS >= 500*time.Millisecond && mcpAVSynced(s)
	})
	sampleMCPAVSync(t, driver, 750*time.Millisecond)
	playingImage := driver.Screenshot()
	if !hasNonBlackPixels(playingImage.Pix) {
		t.Fatal("playing capture remained entirely black")
	}

	driver.Tap(ebiten.KeySpace)
	paused := mcpSnapshot(t, driver)
	if paused.State != avebi.Paused {
		t.Fatalf("state after pause = %v, want Paused", paused.State)
	}
	driver.Tap(ebiten.KeyArrowRight)
	seeked := mcpSnapshot(t, driver)
	wantSeek := min(paused.Position+time.Second, paused.Duration)
	if seeked.State != avebi.Paused {
		t.Fatalf("state after paused seek = %v, want Paused", seeked.State)
	}
	if seeked.FramePTS+50*time.Millisecond < wantSeek || seeked.FramePTS > wantSeek {
		t.Fatalf("frame PTS after seek = %s, want frame covering %s", seeked.FramePTS, wantSeek)
	}
	_ = driver.Screenshot()

	driver.Tap(ebiten.KeySpace)
	waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.State == avebi.Playing && s.Position > seeked.Position+50*time.Millisecond && mcpAVSynced(s)
	})
	driver.Tap(ebiten.KeyS)
	stopped := mcpSnapshot(t, driver)
	if stopped.State != avebi.Stopped || stopped.Position != 0 || stopped.HasEnded {
		t.Fatalf("manual stop = state %v, position %s, ended %v", stopped.State, stopped.Position, stopped.HasEnded)
	}

	driver.Tap(ebiten.KeySpace)
	seekMCPPlayer(t, driver, max(0, stopped.Duration-250*time.Millisecond))
	waitMCPSnapshot(t, driver, 5*time.Second, func(s mcpTortureSnapshot) bool {
		// State and HasEnded are separate synchronized API calls. HasEnded can
		// advance the playback clock to EOF after State was sampled, so wait
		// until the following snapshot observes the stable stopped state too.
		return s.HasEnded && s.State == avebi.Stopped
	})
	driver.Tap(ebiten.KeySpace)
	replayed := waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.State == avebi.Playing && !s.HasEnded && s.Position >= 75*time.Millisecond && s.Position < s.Duration && mcpAVSynced(s)
	})
	if replayed.FramePTS >= replayed.Duration {
		t.Fatalf("replay retained end frame PTS %s", replayed.FramePTS)
	}
	driver.Tap(ebiten.KeyS)

	driver.Tap(ebiten.KeyL)
	driver.Tap(ebiten.KeySpace)
	seekMCPPlayer(t, driver, max(0, stopped.Duration-150*time.Millisecond))
	looped := waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.Looping && s.State == avebi.Playing && s.Position < 500*time.Millisecond && mcpAVSynced(s)
	})
	if looped.HasEnded {
		t.Fatal("looping playback was reported as ended")
	}
	driver.Tap(ebiten.KeyL)
	driver.Tap(ebiten.KeyS)
}

func sampleMCPAVSync(t *testing.T, driver *ebitenmcp.Driver, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		snapshot := mcpSnapshot(t, driver)
		if snapshot.State != avebi.Playing {
			t.Fatalf("player stopped during A/V sync sampling: %#v", snapshot)
		}
		assertMCPAVSync(t, snapshot)
		time.Sleep(25 * time.Millisecond)
	}
}

const mcpAVSyncTolerance = 300 * time.Millisecond

func assertMCPAVSync(t *testing.T, snapshot mcpTortureSnapshot) {
	t.Helper()
	if mcpAVSynced(snapshot) {
		return
	}
	drift := snapshot.FramePTS - snapshot.Position
	if drift < 0 {
		drift = -drift
	}
	t.Fatalf(
		"audio/video drift = %s (audio clock %s, video PTS %s), tolerance %s",
		drift,
		snapshot.Position,
		snapshot.FramePTS,
		mcpAVSyncTolerance,
	)
}

func mcpAVSynced(snapshot mcpTortureSnapshot) bool {
	drift := snapshot.FramePTS - snapshot.Position
	return drift >= -mcpAVSyncTolerance && drift <= mcpAVSyncTolerance
}

func runMCPTorture(t *testing.T, driver *ebitenmcp.Driver) {
	t.Helper()

	cycles := envPositiveInt(t, "AVEBI_MCP_TORTURE_CYCLES", 100)
	warmup := min(10, cycles)
	runMCPCycles(t, driver, warmup)
	base := sampleMCPProcess(t, driver)

	firstHalf := cycles / 2
	runMCPCycles(t, driver, firstHalf)
	middle := sampleMCPProcess(t, driver)
	runMCPCycles(t, driver, cycles-firstHalf)
	end := sampleMCPProcess(t, driver)

	t.Logf("torture cycles=%d base={%s} middle={%s} end={%s}", cycles, base, middle, end)
	maxHeap := uint64(envPositiveInt(t, "AVEBI_MCP_MAX_HEAP_GROWTH_MB", 16)) << 20
	maxRSS := uint64(envPositiveInt(t, "AVEBI_MCP_MAX_RSS_GROWTH_MB", 64)) << 20
	maxGoroutines := envPositiveInt(t, "AVEBI_MCP_MAX_GOROUTINE_GROWTH", 8)
	maxFDs := envPositiveInt(t, "AVEBI_MCP_MAX_FD_GROWTH", 8)

	var problems []string
	if growth(base.heapAlloc, end.heapAlloc) > maxHeap || growth(middle.heapAlloc, end.heapAlloc) > maxHeap {
		problems = append(problems, fmt.Sprintf("heap growth exceeds %s", bytesIEC(maxHeap)))
	}
	if base.rss > 0 && (growth(base.rss, end.rss) > maxRSS || growth(middle.rss, end.rss) > maxRSS) {
		problems = append(problems, fmt.Sprintf("RSS growth exceeds %s", bytesIEC(maxRSS)))
	}
	if end.goroutines-base.goroutines > maxGoroutines {
		problems = append(problems, fmt.Sprintf("goroutines grew by %d", end.goroutines-base.goroutines))
	}
	if base.fds >= 0 && end.fds-base.fds > maxFDs {
		problems = append(problems, fmt.Sprintf("file descriptors grew by %d", end.fds-base.fds))
	}
	if crash := driver.Runtime().Crash(); crash != nil {
		problems = append(problems, fmt.Sprintf("captured panic in %s at tick %d: %v", crash.Phase, crash.Tick, crash.Value))
	}
	if len(problems) > 0 {
		t.Fatalf("torture checks failed: %s\n\n%s", strings.Join(problems, "; "), goroutineDump())
	}
}

func runMCPCycles(t *testing.T, driver *ebitenmcp.Driver, cycles int) {
	t.Helper()
	for cycle := 0; cycle < cycles; cycle++ {
		driver.Tap(ebiten.KeySpace)
		if cycle%10 == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		driver.Tap(ebiten.KeySpace)
		driver.Tap(ebiten.KeyArrowRight)
		driver.Tap(ebiten.KeyArrowLeft)
		if cycle%4 == 0 {
			driver.Tap(ebiten.KeyL)
			driver.Tap(ebiten.KeyL)
		}
		driver.Tap(ebiten.KeySpace)
		driver.Tap(ebiten.KeyS)

		if cycle%10 == 0 {
			snapshot := mcpSnapshot(t, driver)
			if snapshot.Error != "" {
				t.Fatalf("cycle %d: %s", cycle, snapshot.Error)
			}
			if snapshot.State != avebi.Stopped || snapshot.HasEnded {
				t.Fatalf("cycle %d ended in state %v, ended=%v", cycle, snapshot.State, snapshot.HasEnded)
			}
			if crash := driver.Runtime().Crash(); crash != nil {
				t.Fatalf("cycle %d crashed in %s: %v\n%s", cycle, crash.Phase, crash.Value, crash.Stack)
			}
		}
	}
}

func mcpSnapshot(t *testing.T, driver *ebitenmcp.Driver) mcpTortureSnapshot {
	t.Helper()
	var result mcpTortureSnapshot
	driver.WithGame(func(current ebiten.Game) {
		result = current.(*mediaplayer.Game).Snapshot()
	})
	if result.Error != "" {
		t.Fatalf("player state: %s", result.Error)
	}
	return result
}

func waitMCPSnapshot(t *testing.T, driver *ebitenmcp.Driver, timeout time.Duration, condition func(mcpTortureSnapshot) bool) mcpTortureSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last mcpTortureSnapshot
	for {
		last = mcpSnapshot(t, driver)
		if condition(last) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached; last state: %#v", last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func seekMCPPlayer(t *testing.T, driver *ebitenmcp.Driver, position time.Duration) {
	t.Helper()
	var err error
	driver.WithGame(func(current ebiten.Game) {
		err = current.(*mediaplayer.Game).Seek(position)
	})
	if err != nil {
		t.Fatalf("seek player: %v", err)
	}
}

func hasNonBlackPixels(pixels []byte) bool {
	for offset := 0; offset+3 < len(pixels); offset += 4 {
		if pixels[offset] != 0 || pixels[offset+1] != 0 || pixels[offset+2] != 0 {
			return true
		}
	}
	return false
}

type processSample struct {
	heapAlloc  uint64
	heapInuse  uint64
	heapObject uint64
	rss        uint64
	goroutines int
	fds        int
}

func (s processSample) String() string {
	return fmt.Sprintf("heap=%s inuse=%s objects=%d rss=%s goroutines=%d fds=%d",
		bytesIEC(s.heapAlloc), bytesIEC(s.heapInuse), s.heapObject, bytesIEC(s.rss), s.goroutines, s.fds)
}

func sampleMCPProcess(t *testing.T, driver *ebitenmcp.Driver) processSample {
	t.Helper()
	driver.Runtime().Pause()
	defer driver.Runtime().Resume()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	debug.FreeOSMemory()
	runtime.GC()

	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return processSample{
		heapAlloc:  stats.HeapAlloc,
		heapInuse:  stats.HeapInuse,
		heapObject: stats.HeapObjects,
		rss:        processRSS(),
		goroutines: runtime.NumGoroutine(),
		fds:        processFDs(),
	}
}

func processRSS() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	contents, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(contents))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func processFDs() int {
	if runtime.GOOS != "linux" {
		return -1
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func envPositiveInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		t.Fatalf("%s must be a positive integer, got %q", name, value)
	}
	return parsed
}

func growth(before, after uint64) uint64 {
	if after <= before {
		return 0
	}
	return after - before
}

func bytesIEC(value uint64) string {
	const mib = 1 << 20
	return fmt.Sprintf("%.1f MiB", float64(value)/mib)
}

func goroutineDump() string {
	var buffer bytes.Buffer
	if profile := pprof.Lookup("goroutine"); profile != nil {
		_ = profile.WriteTo(&buffer, 2)
	}
	const limit = 24 << 10
	if buffer.Len() > limit {
		return buffer.String()[:limit] + "\n... truncated ..."
	}
	return buffer.String()
}
