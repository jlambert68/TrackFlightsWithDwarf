//go:build opencv

// tracking-worker runs the OpenCV tracker and emits newline-delimited updates.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	tracker := engine.TrackerEngine{Config: engine.TrackerConfig{Input: *input, InputLabel: inputLabel, OutputDir: *output, RecordEvents: true, ExportOriginalVideo: *original, ExportMaskVideo: *mask, FallbackFPS: 30, PreviewFPS: 3, PreviewMaxWidth: 960, Settings: settings}, Stop: stopCh, Hooks: engine.TrackerHooks{
		OnReady: func(info engine.TrackerReady) error {
			return enc.Encode(map[string]any{"type": "ready", "width": info.Width, "height": info.Height, "fps": info.FPS, "totalFrames": info.TotalFrames})
		},
		OnFrame: func(u *engine.FrameUpdate) error {
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
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
