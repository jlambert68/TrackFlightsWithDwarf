package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// trackingNames uses the old tracker's per-event JSON format. Sample names
// live under the data root so the bundled fixture is never modified.
func (s *Server) trackingNames(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		http.NotFound(w, r)
		return
	}
	rootPath, relativeID := s.trackingEventRoot(id)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	event, err := root.OpenRoot(relativeID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer event.Close()
	f, err := event.Open("event.json")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	namesDir := event
	if id == sampleEventID {
		path := filepath.Join(s.dataRoot, "tracking-names")
		if err := os.MkdirAll(path, 0750); err != nil {
			http.Error(w, "cannot open names", 500)
			return
		}
		namesDir, err = os.OpenRoot(path)
		if err != nil {
			http.Error(w, "cannot open names", 500)
			return
		}
		defer namesDir.Close()
	}
	nameFile := "track_names.json"
	if id == sampleEventID {
		nameFile = "sample-event.json"
	}
	s.tracking.namesMu.Lock()
	defer s.tracking.namesMu.Unlock()
	names := map[string]string{}
	f, err = namesDir.Open(nameFile)
	if err == nil {
		err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&names)
		f.Close()
		if err != nil || names == nil {
			http.Error(w, "invalid saved names", 500)
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "cannot read names", 500)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, names)
		return
	}
	track, err := strconv.Atoi(r.PathValue("track"))
	if err != nil || track < 1 || track > 1000000000 {
		http.Error(w, "invalid track", 400)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || len(input.Name) > 256 {
		http.Error(w, "invalid name", 400)
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	key := strconv.Itoa(track)
	if input.Name == "" {
		delete(names, key)
	} else {
		names[key] = input.Name
	}
	data, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		http.Error(w, "cannot save name", 500)
		return
	}
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		http.Error(w, "cannot save name", 500)
		return
	}
	tmp := ".track_names-" + hex.EncodeToString(nonce[:])
	f, err = namesDir.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		http.Error(w, "cannot save name", 500)
		return
	}
	defer namesDir.Remove(tmp)
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = namesDir.Rename(tmp, nameFile)
	}
	if err != nil {
		http.Error(w, "cannot save name", 500)
		return
	}
	writeJSON(w, 200, names)
}
