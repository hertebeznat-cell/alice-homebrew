package homebrew

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var web embed.FS

type loginAttempt struct {
	Count int
	Until time.Time
}
type Server struct {
	Manager  *Manager
	Store    *Store
	Token    string
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func NewServer(manager *Manager, store *Store, token string) *Server {
	return &Server{Manager: manager, Store: store, Token: token, attempts: map[string]loginAttempt{}}
}
func (s *Server) cookie() string {
	if s.Store != nil {
		return "alice_store_session"
	}
	return "alice_station_session"
}
func (s *Server) authed(r *http.Request) bool {
	if s.Token == "" {
		return s.Manager != nil && s.Store == nil
	}
	v := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if v == "" {
		if c, e := r.Cookie(s.cookie()); e == nil {
			v = c.Value
		}
	}
	return subtle.ConstantTimeCompare([]byte(v), []byte(s.Token)) == 1
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
func errorResponse(w http.ResponseWriter, e error, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var storage *StorageError
	if errors.As(e, &storage) {
		w.WriteHeader(http.StatusInsufficientStorage)
		json.NewEncoder(w).Encode(map[string]any{"error": e.Error(), "code": "insufficient_storage", "details": storage.Plan})
		return
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
}
func requestJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON Content-Type required")
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if e != nil {
		return e
	}
	return decode(raw, v)
}
func packageBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, MaxPackage))
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	if r.Method != "GET" && r.Method != "HEAD" {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				errorResponse(w, errors.New("cross-origin request rejected"), 403)
				return
			}
		}
	}
	if r.URL.Path == "/" || r.URL.Path == "/app.js" || r.URL.Path == "/kit.js" || r.URL.Path == "/kit.css" || r.URL.Path == "/style.css" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		b, e := web.ReadFile("web/" + name)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		ct := "text/html; charset=utf-8"
		if strings.HasSuffix(name, ".js") {
			ct = "text/javascript; charset=utf-8"
		}
		if strings.HasSuffix(name, ".css") {
			ct = "text/css; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
		return
	}
	if r.URL.Path == "/api/about" && r.Method == "GET" {
		mode := "station"
		pub := ""
		if s.Store != nil {
			mode = "store"
			pub = s.Store.PublicKey()
		}
		ready := false
		if s.Manager != nil {
			s.Manager.mu.Lock()
			ready = s.Manager.ready
			s.Manager.mu.Unlock()
		}
		jsonResponse(w, map[string]any{"mode": mode, "authenticated": s.authed(r), "access_code_required": s.Token != "", "public_key": pub, "version": Version, "codename": Codename, "commit": Commit, "ready": ready})
		return
	}
	if r.URL.Path == "/api/login" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	if s.Store != nil {
		if (r.URL.Path == "/catalog/v1.json" || r.URL.Path == "/catalog/v2.json") && r.Method == "GET" {
			maxAPI := 1
			if r.URL.Path == "/catalog/v2.json" {
				maxAPI = 2
			}
			v, e := s.Store.CatalogFor(maxAPI)
			if e != nil {
				errorResponse(w, e, 500)
			} else {
				jsonResponse(w, v)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/packages/") && r.Method == "GET" {
			hash := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/packages/"), ".aliceplugin")
			raw, e := s.Store.Download(hash, s.authed(r))
			if e != nil {
				errorResponse(w, e, 404)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment; filename="+hash+".aliceplugin")
			w.Write(raw)
			return
		}
		if r.URL.Path == "/api/submit" && r.Method == "POST" {
			raw, e := packageBody(w, r)
			if e != nil {
				errorResponse(w, e, 400)
				return
			}
			v, e := s.Store.Submit(r.Context(), raw)
			if e != nil {
				errorResponse(w, e, 400)
			} else {
				jsonResponse(w, map[string]any{"id": v.ID, "sha256": v.SHA256, "status": v.Status, "scan": v.Scan})
			}
			return
		}
	}
	if !s.authed(r) {
		errorResponse(w, errors.New("authentication required"), 401)
		return
	}
	if r.URL.Path == "/api/logout" && r.Method == "POST" {
		http.SetCookie(w, &http.Cookie{Name: s.cookie(), Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		jsonResponse(w, map[string]bool{"ok": true})
		return
	}
	var e error
	if s.Store != nil {
		e = s.storeRequest(w, r)
	} else {
		e = s.stationRequest(w, r)
	}
	if e != nil {
		errorResponse(w, e, 400)
	}
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	a := s.attempts[host]
	if time.Now().After(a.Until) {
		a = loginAttempt{Until: time.Now().Add(5 * time.Minute)}
	}
	if a.Count >= 8 {
		s.mu.Unlock()
		errorResponse(w, errors.New("too many login attempts; retry in 5 minutes"), 429)
		return
	}
	a.Count++
	if len(s.attempts) > 1024 {
		s.attempts = map[string]loginAttempt{}
	}
	s.attempts[host] = a
	s.mu.Unlock()
	var q struct {
		Token string `json:"token"`
	}
	if e := requestJSON(w, r, &q); e != nil {
		errorResponse(w, e, 400)
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Token), []byte(s.Token)) != 1 {
		errorResponse(w, errors.New("invalid access code"), 401)
		return
	}
	s.mu.Lock()
	delete(s.attempts, host)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: s.cookie(), Value: s.Token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	jsonResponse(w, map[string]bool{"ok": true})
}
func (s *Server) stationRequest(w http.ResponseWriter, r *http.Request) error {
	m := s.Manager
	if m == nil {
		return errors.New("station manager unavailable")
	}
	switch {
	case r.URL.Path == "/api/audio" && r.Method == "GET":
		jsonResponse(w, m.Audio.Snapshot())
		return nil
	case r.URL.Path == "/api/updates" && r.Method == "GET":
		if m.Updater == nil {
			return errors.New("updater unavailable")
		}
		jsonResponse(w, m.Updater.Status())
		return nil
	case r.URL.Path == "/api/updates/check" && r.Method == "POST":
		if m.Updater == nil {
			return errors.New("updater unavailable")
		}
		// The background request survives a browser navigating away mid-download.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, _ = m.Updater.Check(ctx, true)
		}()
		jsonResponse(w, map[string]bool{"started": true})
		return nil
	case r.URL.Path == "/api/updates/settings" && r.Method == "POST":
		if m.Updater == nil {
			return errors.New("updater unavailable")
		}
		var q struct {
			Enabled bool `json:"enabled"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		if e := m.Updater.SetEnabled(q.Enabled); e != nil {
			return e
		}
		jsonResponse(w, m.Updater.Status())
		return nil
	case r.URL.Path == "/api/yam" && r.Method == "GET":
		if m.Yam == nil {
			return errors.New("yam support unavailable")
		}
		v := m.Yam.Snapshot()
		v["device"] = m.Device
		jsonResponse(w, v)
		return nil
	case r.URL.Path == "/api/yam/refresh" && r.Method == "POST":
		if m.Yam == nil {
			return errors.New("yam support unavailable")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
		defer cancel()
		m.Yam.Refresh(ctx)
		jsonResponse(w, m.Yam.Snapshot())
		return nil
	case r.URL.Path == "/api/yam/local" && r.Method == "POST":
		if m.Yam == nil {
			return errors.New("yam support unavailable")
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxYamUpload))
		if e != nil {
			return e
		}
		job, e := m.Yam.StartLocal(r.URL.Query().Get("file"), raw)
		if e != nil {
			return e
		}
		jsonResponse(w, job)
		return nil
	case r.URL.Path == "/api/yam/jobs" && r.Method == "POST":
		if m.Yam == nil {
			return errors.New("yam support unavailable")
		}
		var q struct {
			Action   string `json:"action"`
			Registry string `json:"registry"`
			Name     string `json:"name"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		job, e := m.Yam.Start(q.Action, q.Registry, q.Name)
		if e != nil {
			return e
		}
		jsonResponse(w, job)
		return nil
	case r.URL.Path == "/api/hardware" && r.Method == "GET":
		jsonResponse(w, HardwareInventory())
		return nil
	case r.URL.Path == "/api/system" && r.Method == "GET":
		jsonResponse(w, m.System())
		return nil
	case r.URL.Path == "/api/status" && r.Method == "GET":
		jsonResponse(w, m.Status())
		return nil
	case r.URL.Path == "/api/storage/plan" && r.Method == "POST":
		var q struct {
			Ticket string `json:"ticket"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		plan, e := m.TicketStorage(q.Ticket)
		if e != nil {
			return e
		}
		jsonResponse(w, plan)
		return nil
	case r.URL.Path == "/api/sources/inspect" && r.Method == "POST":
		var q struct {
			URL  string `json:"url"`
			Key  string `json:"public_key"`
			Name string `json:"name"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		source, c, e := m.PreviewSource(r.Context(), q.URL, q.Key, q.Name)
		if e != nil {
			return e
		}
		jsonResponse(w, map[string]any{"source": source, "plugin_count": len(c.Entries), "signature_verified": true})
		return nil
	case r.URL.Path == "/api/sources" && r.Method == "POST":
		var q struct {
			URL  string `json:"url"`
			Key  string `json:"public_key"`
			Name string `json:"name"`
			Ack  bool   `json:"acknowledge_unofficial"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		source, e := m.AddSource(r.Context(), q.URL, q.Key, q.Name, q.Ack)
		if e != nil {
			return e
		}
		jsonResponse(w, source)
		return nil
	case strings.HasPrefix(r.URL.Path, "/api/sources/") && r.Method == "POST":
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sources/"), "/")
		if len(parts) != 2 {
			return errors.New("invalid source action")
		}
		switch parts[1] {
		case "remove":
			if e := m.ChangeSource(parts[0], false, true); e != nil {
				return e
			}
		case "enabled":
			var q struct {
				Enabled bool `json:"enabled"`
			}
			if e := requestJSON(w, r, &q); e != nil {
				return e
			}
			if e := m.ChangeSource(parts[0], q.Enabled, false); e != nil {
				return e
			}
		case "refresh":
			if e := m.RefreshSource(r.Context(), parts[0]); e != nil {
				return e
			}
		default:
			return errors.New("invalid source action")
		}
		jsonResponse(w, map[string]bool{"ok": true})
		return nil
	case r.URL.Path == "/api/state" && r.Method == "GET":
		jsonResponse(w, m.Snapshot())
		return nil
	case r.URL.Path == "/api/ring" && r.Method == "GET":
		jsonResponse(w, m.Ring.Snapshot())
		return nil
	case r.URL.Path == "/api/inspect" && r.Method == "POST":
		raw, e := packageBody(w, r)
		if e != nil {
			return e
		}
		v, e := m.Stage(raw, false)
		if e != nil {
			return e
		}
		jsonResponse(w, v)
		return nil
	case r.URL.Path == "/api/fetch" && r.Method == "POST":
		var q struct {
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		v, e := m.FetchURL(r.Context(), q.URL, q.SHA256)
		if e != nil {
			return e
		}
		jsonResponse(w, v)
		return nil
	case r.URL.Path == "/api/install" && r.Method == "POST":
		var q struct {
			Ticket           string   `json:"ticket"`
			Permissions      []string `json:"permissions"`
			AcknowledgeLocal bool     `json:"acknowledge_local"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		if e := m.Install(q.Ticket, q.Permissions, q.AcknowledgeLocal); e != nil {
			return e
		}
	case r.URL.Path == "/api/settings" && r.Method == "POST":
		var q struct {
			URL string `json:"catalog_url"`
			Key string `json:"catalog_key"`
			Ack bool   `json:"acknowledge_unofficial"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		if q.URL != "" && !officialSource(q.URL, q.Key) && !q.Ack {
			return errors.New("confirm adding this unofficial source")
		}
		if e := m.SetStore(q.URL, q.Key); e != nil {
			return e
		}
	case r.URL.Path == "/api/catalog/refresh" && r.Method == "POST":
		done := make(chan struct{})
		go func() {
			defer close(done)
			if m.Yam != nil {
				ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
				defer cancel()
				m.Yam.Refresh(ctx)
			}
		}()
		e := m.Refresh(r.Context())
		<-done
		if e != nil {
			return e
		}
	case r.URL.Path == "/api/catalog/inspect" && r.Method == "POST":
		var q struct {
			SHA256   string `json:"sha256"`
			SourceID string `json:"source_id"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		v, e := m.StageFrom(r.Context(), q.SourceID, q.SHA256)
		if e != nil {
			return e
		}
		jsonResponse(w, v)
		return nil
	case strings.HasPrefix(r.URL.Path, "/api/plugins/") && r.Method == "POST":
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/plugins/"), "/")
		if len(parts) != 2 || !idRE.MatchString(parts[0]) {
			return errors.New("invalid plugin action")
		}
		id, action := parts[0], parts[1]
		switch action {
		case "update":
			var q struct {
				Ticket string `json:"ticket"`
				ExpectedSHA string `json:"expected_sha256"`
				Permissions []string `json:"permissions"`
				Acknowledge bool `json:"acknowledge_permissions"`
			}
			if e := requestJSON(w, r, &q); e != nil { return e }
			if e := m.InstallUpdate(q.Ticket, id, q.ExpectedSHA, q.Permissions, q.Acknowledge); e != nil { return e }
		case "enable":
			var q struct {
				Enabled           bool `json:"enabled"`
				AcknowledgeNative bool `json:"acknowledge_native"`
			}
			if e := requestJSON(w, r, &q); e != nil {
				return e
			}
			if e := m.EnableWithConsent(id, q.Enabled, q.AcknowledgeNative); e != nil {
				return e
			}
		case "config":
			var q struct {
				Values map[string]any `json:"values"`
			}
			if e := requestJSON(w, r, &q); e != nil {
				return e
			}
			if e := m.SetConfig(id, q.Values); e != nil {
				return e
			}
		case "remove":
			if e := m.Remove(id); e != nil {
				return e
			}
		case "rollback":
			var q struct {
				Permissions []string `json:"permissions"`
			}
			if e := requestJSON(w, r, &q); e != nil {
				return e
			}
			if e := m.Rollback(id, q.Permissions); e != nil {
				return e
			}
		default:
			return errors.New("unknown plugin action")
		}
	default:
		http.NotFound(w, r)
		return nil
	}
	jsonResponse(w, map[string]bool{"ok": true})
	return nil
}
func (s *Server) storeRequest(w http.ResponseWriter, r *http.Request) error {
	switch {
	case r.URL.Path == "/api/review" && r.Method == "GET":
		jsonResponse(w, map[string]any{"submissions": s.Store.Queue(), "public_key": s.Store.PublicKey()})
		return nil
	case strings.HasPrefix(r.URL.Path, "/api/review/") && r.Method == "POST":
		var q struct {
			Action       string `json:"action"`
			Notes        string `json:"notes"`
			Acknowledged bool   `json:"acknowledged"`
		}
		if e := requestJSON(w, r, &q); e != nil {
			return e
		}
		if e := s.Store.Review(strings.TrimPrefix(r.URL.Path, "/api/review/"), q.Action, q.Notes, q.Acknowledged); e != nil {
			return e
		}
		jsonResponse(w, map[string]bool{"ok": true})
		return nil
	case r.URL.Path == "/api/export" && r.Method == "GET":
		f, e := os.CreateTemp(s.Store.dir, "export-*.zip")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if e = s.Store.Export(f, r.URL.Query().Get("repository")); e != nil {
			return e
		}
		if _, e = f.Seek(0, 0); e != nil {
			return e
		}
		w.Header().Set("Content-Disposition", "attachment; filename=alice-store-github-pages.zip")
		w.Header().Set("Content-Type", "application/zip")
		http.ServeContent(w, r, "alice-store-github-pages.zip", time.Now(), f)
		return nil
	default:
		http.NotFound(w, r)
		return nil
	}
}
