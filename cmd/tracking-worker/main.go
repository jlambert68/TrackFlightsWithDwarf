//go:build opencv

// tracking-worker runs the OpenCV tracker and emits newline-delimited updates.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jlambert/trackflights/internal/objecttracking/engine"
	"gocv.io/x/gocv"
)

func main() {
	input := flag.String("input", "", "input file or RTSP URL")
	mode := flag.String("mode", "file", "file or live")
	output := flag.String("output", "", "event output directory")
	original := flag.Bool("original", false, "record unannotated event video")
	mask := flag.Bool("mask", false, "record motion-mask event video")
	fullVideo := flag.Bool("full-video", false, "save a full annotated video for file input")
	minArea := flag.Float64("min-area", -1, "minimum detection area")
	slowSpeed := flag.Float64("slow-speed", -1, "minimum slow track speed")
	fastSpeed := flag.Float64("fast-speed", -1, "minimum fast track speed")
	threshold := flag.Float64("threshold", -1, "foreground threshold")
	profile := flag.String("profile", "general", "general or rolling_ball")
	flag.Parse()
	if *input == "" || *output == "" || (*mode != "file" && *mode != "live") || (*profile != "general" && *profile != "rolling_ball") {
		fmt.Fprintln(os.Stderr, "invalid tracking arguments")
		os.Exit(2)
	}
	if *fullVideo && *mode != "file" {
		fmt.Fprintln(os.Stderr, "full video export requires a recorded video")
		os.Exit(2)
	}
	if *fullVideo {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			fmt.Fprintln(os.Stderr, "full video export requires ffmpeg")
			os.Exit(1)
		}
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	done := make(chan struct{})
	defer close(done)
	stopCh := make(chan struct{})
	go func() {
		select {
		case <-stop:
			close(stopCh)
		case <-done:
		}
	}()
	enc := json.NewEncoder(os.Stdout)
	lastUpdate := time.Time{}
	inputLabel := "live stream"
	if *mode == "file" {
		inputLabel = "video file"
	}
	settings := engine.DefaultTrackingSettingsForProfile(*profile)
	var sessionDir, fullVideoPath string
	if *fullVideo {
		var err error
		sessionDir, err = os.MkdirTemp(*output, "session-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fullVideoPath = filepath.Join(sessionDir, "tracked.avi")
	}
	if *minArea >= 0 {
		settings.MinArea = *minArea
	}
	if *slowSpeed >= 0 {
		settings.SlowMinSpeed = *slowSpeed
	}
	if *fastSpeed >= 0 {
		settings.MinSpeed = *fastSpeed
	}
	if *threshold >= 0 {
		settings.ForegroundThreshold = *threshold
	}
	if settings.MinArea >= settings.MaxArea || settings.SlowMinSpeed > settings.MinSpeed || settings.ForegroundThreshold > 255 {
		fmt.Fprintln(os.Stderr, "invalid tracking thresholds")
		os.Exit(2)
	}
	frames := 0
	tracker := engine.TrackerEngine{Config: engine.TrackerConfig{Input: *input, InputLabel: inputLabel, OutputDir: *output, FullVideoPath: fullVideoPath, RecordEvents: true, ExportOriginalVideo: *original, ExportMaskVideo: *mask, FallbackFPS: 30, PreviewFPS: 3, PreviewMaxWidth: 960, Settings: settings}, Stop: stopCh, Hooks: engine.TrackerHooks{
		OnReady: func(info engine.TrackerReady) error {
			return enc.Encode(map[string]any{"type": "ready", "width": info.Width, "height": info.Height, "fps": info.FPS, "totalFrames": info.TotalFrames})
		},
		OnFrame: func(u *engine.FrameUpdate) error {
			frames = u.SourceFrame
			if !u.HasDisplay() && time.Since(lastUpdate) < time.Second {
				return nil
			}
			lastUpdate = time.Now()
			data := map[string]any{"type": "frame", "frame": u.SourceFrame, "progress": u.Progress, "fixedLength": u.HasFixedLength, "detections": u.DetectionCount, "tracks": u.TrackCount, "fast": u.FastCount, "slow": u.SlowCount, "recording": u.Recording, "status": u.StatusText}
			if u.HasDisplay() {
				buf, err := gocv.IMEncode(gocv.JPEGFileExt, u.Display)
				if err == nil {
					data["jpeg"] = base64.StdEncoding.EncodeToString(buf.GetBytes())
					buf.Close()
				}
			}
			return enc.Encode(data)
		},
		OnEventSaved: func(dir string) error { return enc.Encode(map[string]any{"type": "event", "directory": dir}) },
	}}
	if err := tracker.Run(); err != nil {
		if sessionDir != "" {
			os.RemoveAll(sessionDir)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if sessionDir != "" {
		if err := exportFullVideo(stopCh, sessionDir, fullVideoPath, filepath.Base(*input), frames, enc); err != nil {
			os.RemoveAll(sessionDir)
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := enc.Encode(map[string]any{"type": "session", "directory": sessionDir}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func exportFullVideo(stop <-chan struct{}, dir, input, source string, frames int, enc *json.Encoder) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = enc.Encode(map[string]any{"type": "export", "status": "Encoding full tracked video"})
	output := filepath.Join(dir, "tracked.mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", input, "-an", "-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", output)
	if data, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("encode tracked video: %w: %s", err, data)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(input); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"type": "tracking_session", "source": source, "frames": frames, "tracked_video": "tracked.mp4", "created_at": time.Now().UTC()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "event.json"), data, 0640)
}
