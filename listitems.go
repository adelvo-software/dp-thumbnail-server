package main

// Video List items (v1.1.0): thumbnail and duration for every item of a vMix
// Video List, WITHOUT touching vMix. For list inputs vMix reports the file
// paths in its XML (<list><item selected="true">D:\Clips\a.mp4</item>…</list>);
// the server runs on the vMix machine and reads the files directly with
// ffmpeg / ffprobe. No SelectIndex, no Preview: a running show is not affected.
//
//   GET /key/<input-key>/items.json   → items with path, name, selected,
//                                       durationMs (ffprobe) and thumb URL
//   GET /key/<input-key>/item/<n>.jpg → frame from item n (1-based)
//
// Only paths that vMix reports for exactly this input are read — no arbitrary
// file access through the URL.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type VmixListItem struct {
	Selected string `xml:"selected,attr"`
	Path     string `xml:",chardata"`
}

type listItemOut struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Selected   bool   `json:"selected"`
	DurationMs int64  `json:"durationMs"` // 0 = unknown (no ffprobe, file missing, stream)
	Exists     bool   `json:"exists"`
	Thumb      string `json:"thumb"`
}

// Per-file cache: key = path + size + mtime, so a replaced clip is read again.
type itemMeta struct {
	durationMs int64
	thumbFile  string
}

var (
	itemMu    sync.Mutex
	itemCache = map[string]*itemMeta{}
	ffSlots   = make(chan struct{}, 2) // at most 2 ffmpeg/ffprobe at a time
)

func fileSig(p string) (string, bool) {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", false
	}
	return fmt.Sprintf("%s|%d|%d", p, st.Size(), st.ModTime().UnixNano()), true
}

// findTool: PATH, otherwise next to the .exe (start.bat suggests ffmpeg in the
// same folder; since Go 1.19 the current directory is no longer searched).
func findTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if ep, err := os.Executable(); err == nil {
		for _, n := range []string{name + ".exe", name} {
			c := filepath.Join(filepath.Dir(ep), n)
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	return ""
}

func runTool(timeout time.Duration, tool string, args ...string) ([]byte, error) {
	ffSlots <- struct{}{}
	defer func() { <-ffSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, tool, args...).Output()
}

func probeDurationMs(p string) int64 {
	tool := findTool("ffprobe")
	if tool == "" {
		return 0
	}
	out, err := runTool(15*time.Second, tool, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", p)
	if err != nil {
		return 0
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || sec <= 0 {
		return 0
	}
	return int64(sec*1000 + 0.5)
}

func metaFor(p string) (*itemMeta, bool) {
	sig, ok := fileSig(p)
	if !ok {
		return nil, false
	}
	itemMu.Lock()
	m := itemCache[sig]
	itemMu.Unlock()
	if m != nil {
		return m, true
	}
	m = &itemMeta{durationMs: probeDurationMs(p)}
	itemMu.Lock()
	itemCache[sig] = m
	itemMu.Unlock()
	return m, true
}

// itemThumb creates a 320x180 frame from the file (once per file version).
// Position: 1 s, or 10 % of the duration for shorter clips; stills: first frame.
func itemThumb(p string, m *itemMeta) (string, error) {
	if m.thumbFile != "" {
		if _, err := os.Stat(m.thumbFile); err == nil {
			return m.thumbFile, nil
		}
	}
	tool := findTool("ffmpeg")
	if tool == "" {
		return "", fmt.Errorf("ffmpeg not found")
	}
	sig, _ := fileSig(p)
	h := sha1.Sum([]byte(sig))
	dir := filepath.Join(thumbDir, "items")
	os.MkdirAll(dir, 0755)
	out := filepath.Join(dir, hex.EncodeToString(h[:])+".jpg")
	ss := "0"
	if m.durationMs > 0 {
		pos := m.durationMs / 10
		if pos > 1000 {
			pos = 1000
		}
		ss = fmt.Sprintf("%.3f", float64(pos)/1000)
	}
	_, err := runTool(20*time.Second, tool, "-y", "-ss", ss, "-i", p, "-frames:v", "1",
		"-vf", "scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2:black",
		"-q:v", "5", out)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(out); err != nil || st.Size() < 100 {
		return "", fmt.Errorf("no frame")
	}
	itemMu.Lock()
	m.thumbFile = out
	itemMu.Unlock()
	return out, nil
}

// listInputs — vMix XML for the list routes, cached for 3 s, so expanding a list
// fetches the XML once instead of once per item thumbnail.
var (
	listXMLMu     sync.Mutex
	listXMLAt     time.Time
	listXMLInputs []VmixInput
)

func listInputs() ([]VmixInput, error) {
	listXMLMu.Lock()
	defer listXMLMu.Unlock()
	if listXMLInputs != nil && time.Since(listXMLAt) < 3*time.Second {
		return listXMLInputs, nil
	}
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(vmixURL)
	if err != nil {
		return nil, fmt.Errorf("cannot reach vMix at %s", vmixURL)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var v VmixXML
	if err := xml.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("invalid XML from vMix")
	}
	listXMLInputs, listXMLAt = v.Inputs.Input, time.Now()
	return listXMLInputs, nil
}

func listItemsForKey(key string) ([]VmixListItem, bool, error) {
	inputs, err := listInputs()
	if err != nil {
		return nil, false, err
	}
	for _, in := range inputs {
		if in.Key == key {
			return in.List, true, nil
		}
	}
	return nil, false, nil
}

// handleListItemRoute serves /key/<key>/items.json and /key/<key>/item/<n>.jpg.
// Returns false for any other path; the normal /key/<key>.jpg handler takes over.
func handleListItemRoute(w http.ResponseWriter, r *http.Request) bool {
	rest := strings.TrimPrefix(r.URL.Path, "/key/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return false
	}
	key := parts[0]
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if len(parts) == 2 && parts[1] == "items.json" {
		items, found, err := listItemsForKey(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if !found {
			http.Error(w, "input not found", http.StatusNotFound)
			return true
		}
		out := make([]listItemOut, 0, len(items))
		// durations in parallel (limited by ffSlots) so large lists do not wait
		res := make([]listItemOut, len(items))
		var wg sync.WaitGroup
		for i, it := range items {
			p := strings.TrimSpace(it.Path)
			res[i] = listItemOut{Index: i + 1, Path: p, Name: filepath.Base(strings.ReplaceAll(p, "\\", "/")),
				Selected: strings.EqualFold(it.Selected, "true"),
				Thumb:    fmt.Sprintf("/key/%s/item/%d.jpg", key, i+1)}
			wg.Add(1)
			go func(i int, p string) {
				defer wg.Done()
				if m, ok := metaFor(p); ok {
					res[i].Exists = true
					res[i].DurationMs = m.durationMs
				}
			}(i, p)
		}
		wg.Wait()
		out = append(out, res...)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"key": key, "items": out, "ffprobe": findTool("ffprobe") != ""})
		return true
	}

	if len(parts) == 3 && parts[1] == "item" && strings.HasSuffix(parts[2], ".jpg") {
		n, err := strconv.Atoi(strings.TrimSuffix(parts[2], ".jpg"))
		if err != nil || n < 1 {
			http.NotFound(w, r)
			return true
		}
		items, found, err := listItemsForKey(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		if !found || n > len(items) {
			http.NotFound(w, r)
			return true
		}
		p := strings.TrimSpace(items[n-1].Path)
		m, ok := metaFor(p)
		if !ok {
			http.Error(w, "file not found on this machine", http.StatusNotFound)
			return true
		}
		f, err := itemThumb(p, m)
		if err != nil {
			http.Error(w, "no thumbnail: "+err.Error(), http.StatusNotFound)
			return true
		}
		d, err := os.ReadFile(f)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return true
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "max-age=60")
		w.Write(d)
		return true
	}
	return false
}
