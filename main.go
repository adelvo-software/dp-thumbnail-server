package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ============================================================
// DP Thumbnail Server
// Generates preview thumbnails for ALL vMix inputs via the
// SnapshotInput API. ffmpeg recommended for resized thumbnails.
//
// https://adelvo.io/directors-plan
// Copyright (c) 2026 Adelvo.
// ============================================================

const VERSION = "1.0.0"

type VmixXML struct {
	XMLName xml.Name   `xml:"vmix"`
	Inputs  VmixInputs `xml:"inputs"`
}
type VmixInputs struct{ Input []VmixInput `xml:"input"` }
type VmixInput struct {
	Key      string `xml:"key,attr"`
	Number   int    `xml:"number,attr"`
	Type     string `xml:"type,attr"`
	Title    string `xml:"title,attr"`
	State    string `xml:"state,attr"`
	Duration int    `xml:"duration,attr"`
}

type ThumbEntry struct {
	Number    int       `json:"number"`
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	Type      string    `json:"type"`
	ThumbFile string    `json:"-"`
	Updated   time.Time `json:"updated"`
}

var (
	mu              sync.RWMutex
	entries         = make(map[int]*ThumbEntry)
	byKey           = make(map[string]*ThumbEntry)
	thumbDir        string
	snapDir         string // where vMix writes snapshots
	vmixBase        string
	vmixURL         string
	serverPort      = "8098"
	autoRefresh     bool
	autoRefreshStop chan struct{}
	skipTypes       = map[string]bool{"audio": true, "audiobusses": true}
)

// --- vMix API ---

func fetchVmixInputs() ([]VmixInput, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(vmixURL)
	if err != nil { return nil, fmt.Errorf("cannot reach vMix at %s — is it running?", vmixURL) }
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var v VmixXML
	if err := xml.Unmarshal(body, &v); err != nil { return nil, fmt.Errorf("invalid XML from vMix") }
	return v.Inputs.Input, nil
}

func snapshot(input VmixInput, force bool) error {
	if skipTypes[strings.ToLower(input.Type)] { return nil }

	thumbPath := filepath.Join(thumbDir, fmt.Sprintf("%d.jpg", input.Number))
	keyPath := ""
	if input.Key != "" {
		keyPath = filepath.Join(thumbDir, fmt.Sprintf("key_%s.jpg", input.Key))
	}

	// Cache check
	if !force {
		if _, err := os.Stat(thumbPath); err == nil {
			e := &ThumbEntry{Number: input.Number, Key: input.Key, Title: input.Title, Type: input.Type, ThumbFile: thumbPath, Updated: fileModTime(thumbPath)}
			mu.Lock(); entries[input.Number] = e; if input.Key != "" { byKey[input.Key] = e }; mu.Unlock()
			return nil
		}
	}

	// Tell vMix to save snapshot to our snap directory
	savePath := filepath.Join(snapDir, fmt.Sprintf("snap_%d.jpg", input.Number))
	apiURL := fmt.Sprintf("%s/api/?Function=SnapshotInput&Input=%d&Value=%s",
		vmixBase, input.Number, url.QueryEscape(savePath))

	resp, err := http.Get(apiURL)
	if err != nil { return fmt.Errorf("API call failed for #%d: %v", input.Number, err) }
	resp.Body.Close()

	// Poll for file (up to 4s)
	var ok bool
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		if info, err := os.Stat(savePath); err == nil && info.Size() > 100 {
			ok = true; break
		}
	}
	if !ok { return fmt.Errorf("vMix did not create snapshot for #%d (%s)", input.Number, input.Title) }

	// Read the full-res snapshot
	data, err := os.ReadFile(savePath)
	if err != nil { return err }

	// Resize to 320x180 if ffmpeg available, otherwise use as-is
	if err := resizeJpeg(savePath, thumbPath); err != nil {
		// No ffmpeg? Just copy the raw snapshot
		os.WriteFile(thumbPath, data, 0644)
	}

	// Copy to key-based path
	if keyPath != "" {
		thumbData, _ := os.ReadFile(thumbPath)
		if thumbData != nil { os.WriteFile(keyPath, thumbData, 0644) }
	}

	// Cleanup temp snapshot
	os.Remove(savePath)

	e := &ThumbEntry{Number: input.Number, Key: input.Key, Title: input.Title, Type: input.Type, ThumbFile: thumbPath, Updated: time.Now()}
	mu.Lock(); entries[input.Number] = e; if input.Key != "" { byKey[input.Key] = e }; mu.Unlock()
	return nil
}

func resizeJpeg(src, dst string) error {
	cmd := exec.Command("ffmpeg", "-y", "-i", src,
		"-vf", "scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2:black",
		"-q:v", "5", dst)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run()
}

func fileModTime(p string) time.Time {
	info, err := os.Stat(p); if err != nil { return time.Time{} }; return info.ModTime()
}

func fileExists(p string) bool { _, e := os.Stat(p); return e == nil }

// --- Generate all ---

func regenerateAll(force bool) (gen, skip, errs int, dur time.Duration) {
	start := time.Now()
	inputs, err := fetchVmixInputs()
	if err != nil { log.Printf("❌ %v", err); return 0, 0, 1, time.Since(start) }
	log.Printf("📋 %d inputs from vMix", len(inputs))

	for _, inp := range inputs {
		if skipTypes[strings.ToLower(inp.Type)] { continue }
		e := snapshot(inp, force)
		if e != nil {
			log.Printf("  ⚠️  %v", e); errs++
		} else {
			mu.RLock(); en := entries[inp.Number]; mu.RUnlock()
			if en != nil && !force && en.Updated.Before(start) { skip++ } else { gen++ }
		}
	}
	dur = time.Since(start)
	log.Printf("✅ %d generated, %d cached, %d errors in %s", gen, skip, errs, dur.Round(time.Millisecond))
	return
}

// --- Auto-refresh loop ---

func autoRefreshLoop(stop chan struct{}) {
	for {
		select { case <-stop: return; default: }

		inputs, err := fetchVmixInputs()
		if err != nil { time.Sleep(5 * time.Second); continue }

		if len(inputs) == 0 { time.Sleep(5 * time.Second); continue }

		for _, inp := range inputs {
			select { case <-stop: return; default: }
			if skipTypes[strings.ToLower(inp.Type)] { continue }
			snapshot(inp, true)
			// ~2 snapshots/sec to not overload vMix
			time.Sleep(500 * time.Millisecond)
		}

		select { case <-stop: return; case <-time.After(2 * time.Second): }
	}
}

// --- HTTP ---

func handleIndex(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/" { w.Header().Set("Content-Type", "text/html; charset=utf-8"); fmt.Fprint(w, indexHTML); return }
	if strings.HasSuffix(p, ".jpg") {
		n := strings.TrimSuffix(strings.TrimPrefix(p, "/"), ".jpg")
		if num, err := strconv.Atoi(n); err == nil { serveThumb(w, num); return }
	}
	http.NotFound(w, r)
}

func serveThumb(w http.ResponseWriter, num int) {
	mu.RLock(); e, ok := entries[num]; mu.RUnlock()
	if !ok { http.Error(w, "Not found", 404); return }
	d, err := os.ReadFile(e.ThumbFile); if err != nil { http.Error(w, "Error", 500); return }
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=10")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(d)
}

func handleKeyThumb(w http.ResponseWriter, r *http.Request) {
	k := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/key/"), ".jpg")
	mu.RLock(); e, ok := byKey[k]; mu.RUnlock()
	if !ok { http.Error(w, "Not found", 404); return }
	d, _ := os.ReadFile(e.ThumbFile)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=10")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(d)
}

func handleRegen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json"); w.Header().Set("Access-Control-Allow-Origin", "*")
	if strings.HasPrefix(r.URL.Path, "/regen/") {
		if num, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/regen/")); err == nil {
			mu.RLock(); e, ok := entries[num]; mu.RUnlock()
			if ok {
				er := snapshot(VmixInput{Number: e.Number, Key: e.Key, Title: e.Title, Type: e.Type}, true)
				if er != nil { json.NewEncoder(w).Encode(map[string]interface{}{"error": er.Error()}) } else {
					json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "input": num})
				}; return
			}
		}
	}
	g, s, e, d := regenerateAll(true)
	json.NewEncoder(w).Encode(map[string]interface{}{"generated": g, "skipped": s, "errors": e, "duration": d.Round(time.Millisecond).String()})
}

func handleThumbnails(w http.ResponseWriter, r *http.Request) {
	mu.RLock(); defer mu.RUnlock()
	type TR struct {
		Number int    `json:"number"`
		Key    string `json:"key"`
		Title  string `json:"title"`
		Type   string `json:"type"`
		Base64 string `json:"base64,omitempty"`
	}
	var out []TR
	for _, e := range entries {
		d, err := os.ReadFile(e.ThumbFile); if err != nil { continue }
		out = append(out, TR{Number: e.Number, Key: e.Key, Title: e.Title, Type: e.Type,
			Base64: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(d)})
	}
	w.Header().Set("Content-Type", "application/json"); w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(out)
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json"); w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == "POST" {
		var s struct {
			VmixHost string `json:"vmixHost"`
			VmixPort string `json:"vmixPort"`
		}
		json.NewDecoder(r.Body).Decode(&s)
		if s.VmixHost != "" && s.VmixPort != "" {
			vmixBase = fmt.Sprintf("http://%s:%s", s.VmixHost, s.VmixPort)
			vmixURL = vmixBase + "/api/"
			log.Printf("⚙️  vMix → %s", vmixURL)
		}
	}
	mu.RLock(); c := len(entries); mu.RUnlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"vmixUrl": vmixURL, "port": serverPort,
		"count": c, "version": VERSION, "autoRefresh": autoRefresh,
	})
}

func handleAutoRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json"); w.Header().Set("Access-Control-Allow-Origin", "*")
	if autoRefresh {
		autoRefresh = false
		if autoRefreshStop != nil { close(autoRefreshStop) }
		log.Println("⏸  Auto-refresh stopped")
	} else {
		autoRefresh = true
		autoRefreshStop = make(chan struct{})
		go autoRefreshLoop(autoRefreshStop)
		log.Println("🔄 Auto-refresh started")
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"autoRefresh": autoRefresh})
}

func handleClear(w http.ResponseWriter, r *http.Request) {
	mu.Lock(); entries = make(map[int]*ThumbEntry); byKey = make(map[string]*ThumbEntry); mu.Unlock()
	os.RemoveAll(thumbDir); os.MkdirAll(thumbDir, 0755)
	w.Header().Set("Content-Type", "application/json"); w.Header().Set("Access-Control-Allow-Origin", "*")
	fmt.Fprint(w, `{"status":"cleared"}`)
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows": cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin": cmd = exec.Command("open", u)
	default: cmd = exec.Command("xdg-open", u)
	}
	cmd.Start()
}

// --- Web UI ---
const indexHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>DP Thumbnail Server — Directors Plan</title>
<style>
:root{--bg:#1c1c1e;--surface:#2c2c2e;--surface2:#3a3a3c;--accent:#e8832a;--accent2:#f5a623;--green:#34c759;--red:#ff3b30;--text:#f5f5f7;--text2:#98989d;--text3:#636366;--radius:10px}
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',system-ui,sans-serif;background:var(--bg);color:var(--text);min-height:100vh}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
.hdr{background:var(--surface);padding:16px 28px;display:flex;align-items:center;justify-content:space-between;border-bottom:1px solid var(--surface2)}
.hdr-left{display:flex;align-items:center;gap:14px}
.hdr h1{font-size:20px;color:var(--accent);font-weight:700;letter-spacing:-.3px}
.hdr h1 span{color:var(--text2);font-weight:400;font-size:14px;margin-left:8px}
.hdr-right{display:flex;align-items:center;gap:16px;font-size:12px;color:var(--text3)}
.hdr-right a{font-size:12px;color:var(--text2);padding:5px 12px;background:var(--surface2);border-radius:6px;transition:all .15s}
.hdr-right a:hover{background:var(--accent);color:#fff;text-decoration:none}
.ct{max-width:1280px;margin:0 auto;padding:24px}
.topbar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-bottom:20px;padding:18px 22px;background:var(--surface);border-radius:var(--radius);border:1px solid var(--surface2)}
.fg{display:flex;flex-direction:column;gap:4px}
.fg label{font-size:10px;color:var(--text3);text-transform:uppercase;letter-spacing:.4px}
.fg input{background:var(--bg);border:1px solid var(--surface2);color:var(--text);padding:8px 11px;border-radius:6px;font-size:13px;outline:none;width:140px;transition:border-color .2s}
.fg input:focus{border-color:var(--accent)}
.fg input.sm{width:60px}
.btn{padding:8px 16px;border:none;border-radius:7px;font-size:13px;font-weight:600;cursor:pointer;transition:all .15s;white-space:nowrap}
.btn-green{background:var(--green);color:#fff}.btn-green:hover{opacity:.85}
.btn-accent{background:var(--accent);color:#fff}.btn-accent:hover{background:var(--accent2)}
.btn-ghost{background:var(--surface2);color:var(--text2)}.btn-ghost:hover{background:var(--text3);color:var(--text)}
.btn-active{background:var(--accent)!important;color:#fff!important}
.btn:disabled{opacity:.4;cursor:not-allowed}
.sep{width:1px;height:32px;background:var(--surface2)}
.stat{font-size:12px;color:var(--text3);margin-left:auto;display:flex;gap:16px;align-items:center}
.stat strong{color:var(--green)}
.stat #vs{max-width:300px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.thumbs{display:grid;grid-template-columns:repeat(auto-fill,minmax(210px,1fr));gap:12px}
.tc{background:var(--surface);border-radius:8px;overflow:hidden;border:1px solid var(--surface2);transition:transform .15s,border-color .15s}
.tc:hover{transform:scale(1.02);border-color:var(--accent)}
.tc{position:relative}
.tc .refresh-hint{display:none;position:absolute;top:4px;right:4px;background:rgba(0,0,0,.7);color:var(--text2);font-size:9px;padding:2px 6px;border-radius:4px;pointer-events:none}
.tc:hover .refresh-hint{display:block}
.ctx{position:fixed;background:var(--surface);border:1px solid var(--surface2);border-radius:8px;padding:4px 0;z-index:999;box-shadow:0 8px 24px rgba(0,0,0,.5);min-width:160px;display:none}
.ctx div{padding:8px 14px;font-size:13px;cursor:pointer;display:flex;align-items:center;gap:8px}
.ctx div:hover{background:var(--surface2)}
.tc{position:relative}
.tc .refresh-overlay{position:absolute;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,.7);display:flex;align-items:center;justify-content:center;opacity:0;pointer-events:none;transition:opacity .2s}
.tc .refresh-overlay.active{opacity:1;pointer-events:auto}
.tc .refresh-overlay span{color:var(--accent);font-size:13px;font-weight:600}
.ctx{position:fixed;background:var(--surface);border:1px solid var(--surface2);border-radius:8px;padding:4px 0;min-width:160px;box-shadow:0 8px 24px rgba(0,0,0,.5);z-index:999;display:none}
.ctx.show{display:block}
.ctx div{padding:8px 14px;font-size:13px;cursor:pointer;display:flex;align-items:center;gap:8px;color:var(--text)}
.ctx div:hover{background:var(--accent);color:#fff}
.tc img{width:100%;aspect-ratio:16/9;object-fit:cover;display:block;background:var(--bg)}
.tc .inf{padding:8px 10px}
.tc .nm{font-size:11px;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-weight:500}
.tc .mt{font-size:10px;color:var(--text3);margin-top:3px}
.sp{display:inline-block;width:12px;height:12px;border:2px solid var(--accent);border-top-color:transparent;border-radius:50%;animation:spin .7s linear infinite;vertical-align:middle;margin-right:4px}
@keyframes spin{to{transform:rotate(360deg)}}
.footer{text-align:center;padding:30px;font-size:11px;color:var(--text3)}.footer a{color:var(--accent)}
.empty{text-align:center;padding:60px 20px;color:var(--text3)}
.empty h3{color:var(--text2);margin-bottom:8px;font-weight:600}
.empty p{font-size:13px;max-width:500px;margin:0 auto;line-height:1.5}
</style></head><body>
<div class="hdr">
<div class="hdr-left"><h1>DP Thumbnail Server<span>v` + VERSION + `</span></h1></div>
<div class="hdr-right">
<span id="connStatus">⏳</span>
<a href="https://adelvo.io/directors-plan" target="_blank">Directors Plan ↗</a>
</div>
</div>
<div class="ct">
<div class="topbar">
<div class="fg"><label>vMix Host</label><input id="sHost" value="localhost"></div>
<div class="fg"><label>Port</label><input id="sPort" value="8088" class="sm"></div>
<button class="btn btn-green" id="bv" onclick="gen()">⚡ Generate All</button>
<button class="btn btn-ghost" id="arBtn" onclick="toggleAR()">🔄 Auto-Refresh</button>
<button class="btn btn-ghost" onclick="ca()">🗑</button>
<div class="sep"></div>
<div class="stat">
<span id="vs">Ready</span>
<span>Thumbs: <strong id="tc">0</strong></span>
</div>
</div>
<div class="thumbs" id="tg"></div>
<div class="empty" id="empty">
<h3>No thumbnails yet</h3>
<p>Make sure vMix is running, enter the host/port above, then click <strong>Generate All</strong>.<br>
Works from any machine on the network — just enter the vMix computer's IP address.<br>
<span style="color:var(--accent)">💡 Install ffmpeg (<code>winget install ffmpeg</code>) for optimized 320×180 thumbnails.</span></p>
</div>
<div class="footer">
<a href="https://adelvo.io/directors-plan" target="_blank">Directors Plan</a> — Professional vMix Control & Automation<br>
<a href="https://adelvo.io/directors-plan/#thumbnail-server" target="_blank">Download &amp; Info</a> · <a href="https://adelvo.io" target="_blank">Adelvo</a>
</div>
</div>
<div class="ctx" id="ctx">
<div onclick="refreshOne()">🔄 Refresh this thumbnail</div>
<div onclick="copyUrl()">📋 Copy thumbnail URL</div>
<div onclick="openFull()">🖼 Open full size</div>
</div>
<script>
let ctxInput=null;
function showCtx(e,num,key,title){e.preventDefault();ctxInput={num,key,title};const m=document.getElementById('ctx');m.style.display='block';m.style.left=Math.min(e.clientX,window.innerWidth-180)+'px';m.style.top=Math.min(e.clientY,window.innerHeight-120)+'px'}
document.addEventListener('click',()=>{document.getElementById('ctx').style.display='none'});
async function refreshOne(){if(!ctxInput)return;const m=document.getElementById('ctx');m.style.display='none';const card=document.querySelector('[data-num="'+ctxInput.num+'"]');if(card){const img=card.querySelector('img');img.style.opacity='.3';const old=card.querySelector('.nm').textContent;card.querySelector('.nm').textContent='⏳ refreshing...';try{await fetch('/regen/'+ctxInput.num);const t=await(await fetch('/thumbnails')).json();const upd=t&&t.find(x=>x.number===ctxInput.num);if(upd){img.src=upd.base64;card.querySelector('.nm').textContent='#'+upd.number+' '+upd.title}else{card.querySelector('.nm').textContent=old}}catch(e){card.querySelector('.nm').textContent=old}img.style.opacity='1'}}
function copyUrl(){if(!ctxInput)return;document.getElementById('ctx').style.display='none';const base=location.origin;const u=ctxInput.key?base+'/key/'+ctxInput.key+'.jpg':base+'/'+ctxInput.num+'.jpg';navigator.clipboard.writeText(u).then(()=>{document.getElementById('vs').textContent='📋 Copied: '+u;setTimeout(()=>{document.getElementById('vs').textContent='Ready'},2000)})}
function openFull(){if(!ctxInput)return;document.getElementById('ctx').style.display='none';const u=ctxInput.key?'/key/'+ctxInput.key+'.jpg':'/'+ctxInput.num+'.jpg';window.open(u,'_blank')}
async function loadSettings(){try{const r=await(await fetch('/settings')).json();const u=new URL(r.vmixUrl||'http://localhost:8088/api/');document.getElementById('sHost').value=u.hostname;document.getElementById('sPort').value=u.port;const n=r.count||0;document.getElementById('connStatus').textContent=n>0?'🟢 vMix · '+n+' inputs':'🟡 Ready';const ab=document.getElementById('arBtn');if(r.autoRefresh){ab.textContent='⏸ Stop';ab.classList.add('btn-active')}else{ab.textContent='🔄 Auto-Refresh';ab.classList.remove('btn-active')}}catch(e){document.getElementById('connStatus').textContent='🔴 Error'}}
async function saveHost(){const b={vmixHost:document.getElementById('sHost').value.trim(),vmixPort:document.getElementById('sPort').value.trim()};await fetch('/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(b)})}
async function gen(){await saveHost();const b=document.getElementById('bv'),s=document.getElementById('vs');b.disabled=1;b.innerHTML='<span class="sp"></span>Generating...';s.innerHTML='<span class="sp"></span>Connecting to vMix...';try{const r=await(await fetch('/regen')).json();s.textContent=r.generated+' new · '+r.skipped+' cached · '+r.errors+' errors · '+r.duration;lt();loadSettings()}catch(e){s.textContent='Error: '+e.message}b.disabled=0;b.textContent='⚡ Generate All'}
async function lt(){try{const t=await(await fetch('/thumbnails')).json();const n=t?t.length:0;document.getElementById('tc').textContent=n;document.getElementById('empty').style.display=n?'none':'block';if(!t||!n){document.getElementById('tg').innerHTML='';return}t.sort((a,b)=>a.number-b.number);document.getElementById('tg').innerHTML=t.map(x=>'<div class="tc" data-num="'+x.number+'" oncontextmenu="showCtx(event,'+x.number+',\''+x.key+'\',\''+x.title.replace(/'/g,"\\'")+'\')"><img src="'+x.base64+'" loading="lazy"><div class="refresh-hint">Right-click to refresh</div><div class="inf"><div class="nm">#'+x.number+' '+x.title+'</div><div class="mt">'+x.type+'</div></div></div>').join('')}catch(e){}}
async function toggleAR(){await saveHost();try{await(await fetch('/autorefresh',{method:'POST'})).json();loadSettings()}catch(e){}}
async function ca(){if(!confirm('Clear all?'))return;await fetch('/clear');lt();loadSettings()}
loadSettings();lt();
</script></body></html>`

// --- Main ---

func main() {
	ep, _ := os.Executable()
	exeDir := filepath.Dir(ep)
	thumbDir = filepath.Join(exeDir, "thumbnails")
	snapDir = filepath.Join(exeDir, "snapshots")
	os.MkdirAll(thumbDir, 0755)
	os.MkdirAll(snapDir, 0755)

	vmixHost, vmixPort := "localhost", "8088"
	if len(os.Args) > 1 { serverPort = os.Args[1] }
	if len(os.Args) > 2 { vmixHost = os.Args[2] }
	if len(os.Args) > 3 { vmixPort = os.Args[3] }

	vmixBase = fmt.Sprintf("http://%s:%s", vmixHost, vmixPort)
	vmixURL = vmixBase + "/api/"

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/key/", handleKeyThumb)
	http.HandleFunc("/regen", handleRegen)
	http.HandleFunc("/regen/", handleRegen)
	http.HandleFunc("/thumbnails", handleThumbnails)
	http.HandleFunc("/settings", handleSettings)
	http.HandleFunc("/autorefresh", handleAutoRefresh)
	http.HandleFunc("/clear", handleClear)

	serverURL := fmt.Sprintf("http://localhost:%s", serverPort)
	log.Printf("🎬 DP Thumbnail Server v%s", VERSION)
	log.Printf("   %s", serverURL)
	log.Printf("   vMix: %s", vmixURL)

	go func() { time.Sleep(500 * time.Millisecond); openBrowser(serverURL) }()

	// Auto-generate on startup
	go func() {
		time.Sleep(2 * time.Second)
		log.Println("🔄 Auto-generating thumbnails from vMix...")
		g, s, e, d := regenerateAll(false)
		log.Printf("🏁 %d new, %d cached, %d errors (%s)", g, s, e, d.Round(time.Millisecond))
	}()

	log.Fatal(http.ListenAndServe("0.0.0.0:"+serverPort, nil))
}
