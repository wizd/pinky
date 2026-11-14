package gateway

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/history"
	"github.com/DIYgod/pinky/internal/recording"
)

// Smaller recordings: when a task's recording is finished, a worker encodes
// it again smaller (recording.Smaller) and puts the new file in its place,
// one recording at a time, while Pinky runs. The recorder itself stays as
// it is: it remuxes the phone's stream as it comes, so the replay of a
// running task and what reads vouch for (video.go) do not change, and the
// phone's stream, which the mirror and the screen reads share, keeps its
// quality. A recording whose new file would be larger, or that a replay
// holds open on Windows, keeps its original; one still waiting when Pinky
// quits stays as recorded. docs/notes/recording-size.md has the numbers.

// shrinkQueue is how many finished recordings may wait for the worker;
// more are kept as recorded.
const shrinkQueue = 64

type shrinkJob struct {
	taskID string
	path   string
}

type shrinker struct {
	once  sync.Once
	jobs  chan shrinkJob
	ok    bool // this computer can encode
	small bool // guarded by Gateway.mu: shrink recordings that finish
}

// SetSmallerRecordings sets whether recordings are made smaller when their
// task ends (true), or kept as the phone sent them (false). It applies to
// the recordings that finish from now on.
func (g *Gateway) SetSmallerRecordings(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.shrink.small = on
}

// shrinkLater queues a finished recording to be made smaller, when the
// person chose smaller recordings.
func (g *Gateway) shrinkLater(taskID, path string) {
	g.mu.Lock()
	on, closed := g.shrink.small, g.closed
	g.mu.Unlock()
	if !on || closed || g.store == nil {
		return
	}
	g.shrink.once.Do(func() {
		if err := recording.ShrinkAvailable(); err != nil {
			g.log.Info("screen recording: recordings stay as recorded", "err", err)
			return
		}
		g.shrink.ok = true
		g.shrink.jobs = make(chan shrinkJob, shrinkQueue)
		// Files left half written when Pinky last quit.
		if err := g.store.RemoveRecordingTemps(); err != nil {
			g.log.Warn("screen recording: remove unfinished files", "err", err)
		}
		g.wg.Add(1)
		go g.shrinkLoop()
	})
	if !g.shrink.ok {
		return
	}
	select {
	case g.shrink.jobs <- shrinkJob{taskID: taskID, path: path}:
		g.shrinks.add()
	default:
		g.log.Info("screen recording: too many recordings to shrink; this one stays as recorded", "task", taskID)
	}
}

func (g *Gateway) shrinkLoop() {
	defer g.wg.Done()
	for {
		select {
		case <-g.ctx.Done():
			// What still waits stays as recorded.
			for {
				select {
				case <-g.shrink.jobs:
					g.shrinks.done()
				default:
					return
				}
			}
		case j := <-g.shrink.jobs:
			g.shrinkOne(j)
			g.shrinks.done()
		}
	}
}

// shrinkOne makes one recording smaller.
func (g *Gateway) shrinkOne(j shrinkJob) {
	start := time.Now()
	tmp := history.RecordingTemp(j.path)
	if err := recording.Shrink(g.ctx, j.path, tmp, recording.Smaller); err != nil {
		if g.ctx.Err() == nil {
			g.log.Warn("screen recording: shrink", "task", j.taskID, "err", err)
		}
		return
	}
	before, err1 := os.Stat(j.path)
	after, err2 := os.Stat(tmp)
	if err1 != nil || err2 != nil || after.Size() >= before.Size() {
		os.Remove(tmp) // gone meanwhile, or the phone's stream was smaller
		return
	}
	info, err := recording.Probe(tmp)
	if err != nil {
		os.Remove(tmp)
		g.log.Warn("screen recording: shrink", "task", j.taskID, "err", err)
		return
	}
	// The row the recorder saved first: replacing checks it is still there.
	g.rec.flush()
	rec := history.Recording{TaskID: j.taskID, Path: j.path, Length: info.Length, Codec: string(driver.H264),
		Width: info.Width, Height: info.Height, Size: after.Size()}
	ctx, cancel := context.WithTimeout(g.ctx, 10*time.Second)
	defer cancel()
	replaced, err := g.store.ReplaceRecording(ctx, rec)
	if err != nil {
		g.log.Warn("screen recording: replace with the smaller file", "task", j.taskID, "err", err)
		return
	}
	if replaced {
		g.log.Info("screen recording: made smaller", "task", j.taskID, "bytes", before.Size(), "now", after.Size(),
			"took", time.Since(start).Round(time.Millisecond))
	}
}
