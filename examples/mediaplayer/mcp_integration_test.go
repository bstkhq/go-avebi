//go:build !ios && !android && (amd64 || arm64)

package main

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
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
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
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
				return &mcpTortureGame{}
			}
			game, err := newMCPTortureGame(mcpTestMedia)
			if err != nil {
				return &mcpTortureGame{startupErr: err}
			}
			return game
		},
		ebitenmcp.WithName("avebi-ffgo-torture"),
		ebitenmcp.WithCaptureStage(ebitenmcp.StageOffscreen),
		ebitenmcp.WithState("player", func(current ebiten.Game) any {
			return current.(*mcpTortureGame).snapshot()
		}),
	)
}

type mcpTortureGame struct {
	player     *avebi.Player
	frame      *ebiten.Image
	position   time.Duration
	duration   time.Duration
	state      avebi.PlaybackState
	lastErr    error
	startupErr error
	closed     bool
	updates    uint64
}

type mcpTortureSnapshot struct {
	State    avebi.PlaybackState
	Position time.Duration
	Duration time.Duration
	FramePTS time.Duration
	HasEnded bool
	HasAudio bool
	Looping  bool
	Closed   bool
	Updates  uint64
	Err      error
}

func newMCPTortureGame(mediaPath string) (*mcpTortureGame, error) {
	player, err := avebi.NewPlayer(mediaPath)
	if err != nil {
		return nil, err
	}
	return &mcpTortureGame{
		player:   player,
		duration: player.Duration(),
		state:    avebi.Stopped,
	}, nil
}

func (g *mcpTortureGame) Update() error {
	g.updates++
	if g.startupErr != nil {
		return g.startupErr
	}
	if g.closed || g.player == nil {
		return nil
	}

	frame, err := g.player.CurrentFrame()
	if err != nil {
		g.lastErr = err
		return err
	}
	g.frame = frame
	g.position, err = g.player.Position()
	if err != nil {
		g.lastErr = err
		return err
	}
	g.state, err = g.player.State()
	if err != nil {
		g.lastErr = err
		return err
	}

	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeySpace):
		if g.state == avebi.Playing {
			err = g.player.Pause()
		} else {
			err = g.player.Play()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyS):
		err = g.player.Stop()
	case inpututil.IsKeyJustPressed(ebiten.KeyL):
		g.player.SetLooping(!g.player.GetLooping())
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowRight):
		err = g.player.Seek(min(g.duration, g.position+500*time.Millisecond))
	case inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft):
		err = g.player.Seek(max(0, g.position-500*time.Millisecond))
	}
	if err != nil {
		g.lastErr = err
		return err
	}
	if err := g.player.Error(); err != nil {
		g.lastErr = err
		return err
	}
	return nil
}

func (g *mcpTortureGame) Draw(screen *ebiten.Image) {
	screen.Fill(color.Black)
	if g.frame != nil {
		avebi.Draw(screen, g.frame)
	}
}

func (*mcpTortureGame) Layout(int, int) (int, int) { return 640, 360 }

func (g *mcpTortureGame) snapshot() mcpTortureSnapshot {
	result := mcpTortureSnapshot{
		State:    g.state,
		Position: g.position,
		Duration: g.duration,
		Closed:   g.closed,
		Updates:  g.updates,
		Err:      errors.Join(g.startupErr, g.lastErr),
	}
	if g.player != nil {
		result.FramePTS = g.player.LastPresentationOffset()
		result.HasEnded = g.player.HasEnded()
		result.HasAudio = g.player.HasAudio()
		result.Looping = g.player.GetLooping()
		result.Err = errors.Join(result.Err, g.player.Error())
	}
	return result
}

func (g *mcpTortureGame) close() error {
	if g.closed {
		return nil
	}
	g.closed = true
	if g.player == nil {
		return nil
	}
	return g.player.Close()
}

func TestFFGOMCPIntegrationAndTorture(t *testing.T) {
	if os.Getenv(mcpTestMediaEnv) == "" {
		t.Skip("set AVEBI_MCP_TEST_MEDIA to an H.264/AAC file")
	}

	driver := ebitenmcp.T(t)
	driver.Timeout = 30 * time.Second
	t.Cleanup(func() {
		driver.WithGame(func(current ebiten.Game) {
			if err := current.(*mcpTortureGame).close(); err != nil {
				t.Errorf("close torture player: %v", err)
			}
		})
	})

	initial := mcpSnapshot(t, driver)
	if initial.Err != nil {
		t.Fatalf("start player: %v", initial.Err)
	}
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

	driver.Tap(ebiten.KeySpace)
	waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.State == avebi.Playing && s.Position >= 75*time.Millisecond
	})
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
	wantSeek := min(paused.Position+500*time.Millisecond, paused.Duration)
	if seeked.State != avebi.Paused {
		t.Fatalf("state after paused seek = %v, want Paused", seeked.State)
	}
	if seeked.FramePTS+50*time.Millisecond < wantSeek || seeked.FramePTS > wantSeek {
		t.Fatalf("frame PTS after seek = %s, want frame covering %s", seeked.FramePTS, wantSeek)
	}
	_ = driver.Screenshot()

	driver.Tap(ebiten.KeySpace)
	waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.State == avebi.Playing && s.Position > seeked.Position+50*time.Millisecond
	})
	driver.Tap(ebiten.KeyS)
	stopped := mcpSnapshot(t, driver)
	if stopped.State != avebi.Stopped || stopped.Position != 0 || stopped.HasEnded {
		t.Fatalf("manual stop = state %v, position %s, ended %v", stopped.State, stopped.Position, stopped.HasEnded)
	}

	driver.Tap(ebiten.KeySpace)
	withMCPPlayer(t, driver, func(player *avebi.Player) error {
		return player.Seek(max(0, player.Duration()-250*time.Millisecond))
	})
	waitMCPSnapshot(t, driver, 5*time.Second, func(s mcpTortureSnapshot) bool {
		// State and HasEnded are separate synchronized API calls. HasEnded can
		// advance the playback clock to EOF after State was sampled, so wait
		// until the following snapshot observes the stable stopped state too.
		return s.HasEnded && s.State == avebi.Stopped
	})
	driver.Tap(ebiten.KeySpace)
	replayed := waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.State == avebi.Playing && !s.HasEnded && s.Position >= 75*time.Millisecond && s.Position < s.Duration
	})
	if replayed.FramePTS >= replayed.Duration {
		t.Fatalf("replay retained end frame PTS %s", replayed.FramePTS)
	}
	driver.Tap(ebiten.KeyS)

	driver.Tap(ebiten.KeyL)
	driver.Tap(ebiten.KeySpace)
	withMCPPlayer(t, driver, func(player *avebi.Player) error {
		return player.Seek(max(0, player.Duration()-150*time.Millisecond))
	})
	looped := waitMCPSnapshot(t, driver, 3*time.Second, func(s mcpTortureSnapshot) bool {
		return s.Looping && s.State == avebi.Playing && s.Position < 500*time.Millisecond
	})
	if looped.HasEnded {
		t.Fatal("looping playback was reported as ended")
	}
	driver.Tap(ebiten.KeyL)
	driver.Tap(ebiten.KeyS)
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
			if snapshot.Err != nil {
				t.Fatalf("cycle %d: %v", cycle, snapshot.Err)
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
		result = current.(*mcpTortureGame).snapshot()
	})
	if result.Err != nil {
		t.Fatalf("player state: %v", result.Err)
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

func withMCPPlayer(t *testing.T, driver *ebitenmcp.Driver, operation func(*avebi.Player) error) {
	t.Helper()
	var err error
	driver.WithGame(func(current ebiten.Game) {
		game := current.(*mcpTortureGame)
		err = operation(game.player)
		if err == nil {
			game.position, _ = game.player.Position()
			game.state, _ = game.player.State()
		}
	})
	if err != nil {
		t.Fatalf("player operation: %v", err)
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
