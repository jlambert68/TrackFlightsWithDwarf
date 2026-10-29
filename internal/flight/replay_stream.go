package flight

import (
	"bufio"
	"container/heap"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jlambert/trackflights/internal/flight/tracking"
)

const replayRunSize = 8192
const replayMergeFanIn = 32
const replayWindowDuration = 5 * time.Minute

// StreamReplay is the compatibility stream. Index sorting is disk-backed, so
// memory does not grow with the number of frames in a recording.
func StreamReplay(ctx context.Context, root, date string, receiver Receiver, header func(Replay) error, batch func([]ReplayEvent) error, expiry ...time.Duration) error {
	return streamReplay(ctx, root, date, receiver, nil, nil, nil, normalizedAircraftExpiry(expiry...), header, batch)
}

// StreamReplayWindow returns up to five minutes of events around fromMillis.
// Snapshots of active aircraft just before the window preserve sparse fields.
func StreamReplayWindow(ctx context.Context, root, date string, receiver Receiver, fromMillis *int64, header func(Replay) error, batch func([]ReplayEvent) error, expiry ...time.Duration) error {
	if fromMillis == nil {
		v := int64(-1)
		fromMillis = &v
	}
	return streamReplay(ctx, root, date, receiver, fromMillis, nil, nil, normalizedAircraftExpiry(expiry...), header, batch)
}

// StreamReplayTracks reconstructs only position fixes for the requested aircraft,
// before the replay window. It uses the same disk-backed ordering and decoder as
// replay, without sending or retaining all earlier receiver frames.
func StreamReplayTracks(ctx context.Context, root, date string, receiver Receiver, icaos []string, before int64, batch func([]ReplayEvent) error, expiry ...time.Duration) error {
	filter := make(map[string]bool, len(icaos))
	for _, icao := range icaos {
		filter[strings.ToUpper(icao)] = true
	}
	last := map[string]ReplayAircraft{}
	return streamReplay(ctx, root, date, receiver, nil, filter, &before, normalizedAircraftExpiry(expiry...), func(Replay) error { return nil }, func(events []ReplayEvent) error {
		points := make([]ReplayEvent, 0, len(events))
		for _, event := range events {
			a := event.Aircraft
			if a.Latitude == nil || a.Longitude == nil || a.PositionTimestamp.IsZero() || a.PositionTimestamp.UnixMilli() >= before {
				continue
			}
			previous, exists := last[a.ICAO]
			if exists && previous.PositionTimestamp.Equal(a.PositionTimestamp) && *previous.Latitude == *a.Latitude && *previous.Longitude == *a.Longitude {
				continue
			}
			last[a.ICAO] = a
			points = append(points, event)
		}
		if len(points) > 0 {
			return batch(points)
		}
		return nil
	})
}

type replayIndexRecord struct {
	Nanos  int64
	File   uint32
	Offset int64
	Length uint32
}

func lessRecord(a, b replayIndexRecord) bool {
	if a.Nanos != b.Nanos {
		return a.Nanos < b.Nanos
	}
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Offset < b.Offset
}
func writeRecord(w io.Writer, r replayIndexRecord) error {
	return binary.Write(w, binary.LittleEndian, r)
}
func readRecord(r io.Reader) (replayIndexRecord, error) {
	var item replayIndexRecord
	err := binary.Read(r, binary.LittleEndian, &item)
	return item, err
}

type runReader struct {
	File   *os.File
	reader *bufio.Reader
	item   replayIndexRecord
}
type runHeap []*runReader

func (h runHeap) Len() int           { return len(h) }
func (h runHeap) Less(i, j int) bool { return lessRecord(h[i].item, h[j].item) }
func (h runHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *runHeap) Push(x any)        { *h = append(*h, x.(*runReader)) }
func (h *runHeap) Pop() any          { old := *h; item := old[len(old)-1]; *h = old[:len(old)-1]; return item }

func writeReplayRun(ctx context.Context, dir string, records []replayIndexRecord) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sort.Slice(records, func(i, j int) bool { return lessRecord(records[i], records[j]) })
	File, err := os.CreateTemp(dir, "run-")
	if err != nil {
		return "", err
	}
	w := bufio.NewWriter(File)
	for _, record := range records {
		if err = writeRecord(w, record); err != nil {
			break
		}
	}
	if err == nil {
		err = w.Flush()
	}
	err = errors.Join(err, File.Close())
	return File.Name(), err
}

func mergeReplayRuns(ctx context.Context, dir string, paths []string) (string, error) {
	out, err := os.CreateTemp(dir, "merge-")
	if err != nil {
		return "", err
	}
	writer := bufio.NewWriter(out)
	readers := []*runReader{}
	q := runHeap{}
	defer func() {
		for _, r := range readers {
			_ = r.File.Close()
		}
	}()
	for _, path := range paths {
		f, e := os.Open(path)
		if e != nil {
			out.Close()
			return "", e
		}
		r := &runReader{File: f, reader: bufio.NewReader(f)}
		readers = append(readers, r)
		r.item, e = readRecord(r.reader)
		if e == nil {
			heap.Push(&q, r)
		} else if !errors.Is(e, io.EOF) {
			out.Close()
			return "", e
		}
	}
	for q.Len() > 0 {
		if err = ctx.Err(); err != nil {
			break
		}
		r := heap.Pop(&q).(*runReader)
		if err = writeRecord(writer, r.item); err != nil {
			break
		}
		r.item, err = readRecord(r.reader)
		if err == nil {
			heap.Push(&q, r)
		} else if errors.Is(err, io.EOF) {
			err = nil
		} else {
			break
		}
	}
	if err == nil {
		err = writer.Flush()
	}
	err = errors.Join(err, out.Close())
	return out.Name(), err
}

func streamReplay(ctx context.Context, root, date string, receiver Receiver, windowStart *int64, filter map[string]bool, before *int64, expiry time.Duration, header func(Replay) error, batch func([]ReplayEvent) error) error {
	if !validDate(date) {
		return errors.New("invalid replay date")
	}
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return header(Replay{Date: date, AircraftExpirySeconds: int64(expiry / time.Second)})
	}
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "trackflights-replay-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	info := Replay{Date: date, AircraftExpirySeconds: int64(expiry / time.Second)}
	paths := []string{}
	levels := [][]string{}
	records := make([]replayIndexRecord, 0, replayRunSize)
	var addRun func(string, int) error
	addRun = func(path string, level int) error {
		if level == len(levels) {
			levels = append(levels, nil)
		}
		levels[level] = append(levels[level], path)
		if len(levels[level]) < replayMergeFanIn {
			return nil
		}
		inputs := levels[level]
		merged, err := mergeReplayRuns(ctx, tmp, inputs)
		if err != nil {
			return err
		}
		for _, input := range inputs {
			if err := os.Remove(input); err != nil {
				return err
			}
		}
		levels[level] = nil
		return addRun(merged, level+1)
	}
	flushRun := func() error {
		if len(records) == 0 {
			return nil
		}
		path, err := writeReplayRun(ctx, tmp, records)
		if err != nil {
			return err
		}
		if err := addRun(path, 0); err != nil {
			return err
		}
		records = records[:0]
		return nil
	}
	for _, dir := range dirs {
		if !dir.IsDir() || !validICAO(dir.Name()) || (filter != nil && !filter[strings.ToUpper(dir.Name())]) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(root, dir.Name(), date+".jsonl")
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		fileID := uint32(len(paths))
		paths = append(paths, path)
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 64*1024), 1024*1024)
		var Offset, tokenOffset int64
		var tokenLength int
		s.Split(func(data []byte, eof bool) (int, []byte, error) {
			n, t, e := bufio.ScanLines(data, eof)
			if t != nil {
				tokenOffset, tokenLength = Offset, n
			}
			Offset += int64(n)
			return n, t, e
		})
		for s.Scan() {
			if err = ctx.Err(); err != nil {
				f.Close()
				return err
			}
			entry, valid := parseReplayEntry(s.Bytes(), dir.Name())
			if !valid {
				info.SkippedLines++
				continue
			}
			if entry.Event != "" && entry.Event != "adsb_frame" && entry.Event != "map_position_set" {
				continue
			}
			if entry.Event == "map_position_set" && entry.WorldPosition == nil {
				continue
			}
			ms := entry.ReceivedAt.UnixMilli()
			if (before != nil && ms >= *before) || (filter != nil && !filter[entry.ICAO]) {
				continue
			}
			if info.StartMilliseconds == 0 || ms < info.StartMilliseconds {
				info.StartMilliseconds = ms
			}
			if ms > info.EndMilliseconds {
				info.EndMilliseconds = ms
			}
			records = append(records, replayIndexRecord{Nanos: entry.ReceivedAt.UnixNano(), File: fileID, Offset: tokenOffset, Length: uint32(tokenLength)})
			if len(records) == cap(records) {
				if err := flushRun(); err != nil {
					f.Close()
					return err
				}
			}
		}
		err = errors.Join(s.Err(), f.Close())
		if err != nil {
			return err
		}
	}
	if err := flushRun(); err != nil {
		return err
	}
	runs := []string{}
	for _, level := range levels {
		runs = append(runs, level...)
	}
	for len(runs) > 1 {
		next := []string{}
		for i := 0; i < len(runs); i += replayMergeFanIn {
			end := min(i+replayMergeFanIn, len(runs))
			path, err := mergeReplayRuns(ctx, tmp, runs[i:end])
			if err != nil {
				return err
			}
			for _, input := range runs[i:end] {
				if err := os.Remove(input); err != nil {
					return err
				}
			}
			next = append(next, path)
		}
		runs = next
	}
	start, end := info.StartMilliseconds, info.EndMilliseconds
	if windowStart != nil && len(runs) > 0 {
		if *windowStart >= 0 {
			start = max(info.StartMilliseconds, min(*windowStart, info.EndMilliseconds))
		}
		end = min(info.EndMilliseconds, start+replayWindowDuration.Milliseconds())
	}
	info.WindowStartMilliseconds, info.WindowEndMilliseconds = start, end
	if err := header(info); err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	indexFile, err := os.Open(runs[0])
	if err != nil {
		return err
	}
	defer indexFile.Close()
	indexReader := bufio.NewReader(indexFile)
	decoder := newReplayDecoder(receiver, expiry)
	events := make([]ReplayEvent, 0, 128)
	snapshots := map[string]ReplayEvent{}
	snapshotSent := false
	var nextPrune int64
	files := map[uint32]*os.File{}
	order := []uint32{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	readSource := func(item replayIndexRecord) (tracking.Entry, error) {
		f := files[item.File]
		if f == nil {
			if len(order) >= 16 {
				_ = files[order[0]].Close()
				delete(files, order[0])
				order = order[1:]
			}
			var e error
			f, e = os.Open(paths[item.File])
			if e != nil {
				return tracking.Entry{}, e
			}
			files[item.File] = f
			order = append(order, item.File)
		}
		data := make([]byte, item.Length)
		if _, e := f.ReadAt(data, item.Offset); e != nil {
			return tracking.Entry{}, e
		}
		entry, ok := parseReplayEntry(data, filepath.Base(filepath.Dir(paths[item.File])))
		if !ok || entry.ReceivedAt.UnixNano() != item.Nanos {
			return tracking.Entry{}, errors.New("recording changed during replay")
		}
		return entry, nil
	}
	emit := func(e ReplayEvent) error {
		events = append(events, e)
		if len(events) == cap(events) {
			if err := batch(events); err != nil {
				return err
			}
			events = make([]ReplayEvent, 0, 128)
		}
		return nil
	}
	emitSnapshots := func() error {
		list := make([]ReplayEvent, 0, len(snapshots))
		for _, e := range snapshots {
			if e.Milliseconds >= start-expiry.Milliseconds() {
				list = append(list, e)
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Milliseconds == list[j].Milliseconds {
				return list[i].Aircraft.ICAO < list[j].Aircraft.ICAO
			}
			return list[i].Milliseconds < list[j].Milliseconds
		})
		for _, e := range list {
			if err := emit(e); err != nil {
				return err
			}
		}
		snapshotSent = true
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, err := readRecord(indexReader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		ms := time.Unix(0, item.Nanos).UnixMilli()
		if windowStart != nil && ms >= start && !snapshotSent {
			if err := emitSnapshots(); err != nil {
				return err
			}
		}
		if windowStart != nil && ms > end {
			break
		}
		if ms >= nextPrune {
			for icao, seen := range decoder.last {
				if ms-seen.UnixMilli() > expiry.Milliseconds() {
					delete(decoder.last, icao)
					delete(decoder.states, icao)
					delete(decoder.cprStates, icao)
					delete(snapshots, icao)
				}
			}
			nextPrune = ms + 60000
		}
		entry, err := readSource(item)
		if err != nil {
			return err
		}
		event, ok := decoder.apply(entry)
		if !ok {
			continue
		}
		if windowStart != nil && ms < start {
			snapshots[entry.ICAO] = event
			continue
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	if windowStart != nil && !snapshotSent {
		if err := emitSnapshots(); err != nil {
			return err
		}
	}
	if len(events) > 0 {
		return batch(events)
	}
	return nil
}

func parseReplayEntry(line []byte, folder string) (tracking.Entry, bool) {
	var entry tracking.Entry
	if json.Unmarshal(line, &entry) != nil || entry.ReceivedAt.IsZero() {
		return entry, false
	}
	if entry.ICAO == "" {
		entry.ICAO = strings.ToUpper(folder)
	}
	entry.ICAO = strings.ToUpper(entry.ICAO)
	return entry, validICAO(entry.ICAO)
}
