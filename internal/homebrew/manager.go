package homebrew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Saved struct {
	Manifest       Manifest       `json:"manifest"`
	SHA256         string         `json:"sha256"`
	Config         map[string]any `json:"config"`
	Reviewed       bool           `json:"reviewed"`
	SourceID       string         `json:"source_id,omitempty"`
	SourceName     string         `json:"source_name,omitempty"`
	SourceOfficial bool           `json:"source_official,omitempty"`
	NativeConsent  bool           `json:"native_consent,omitempty"`
}
type Installed struct {
	Saved
	Enabled         bool     `json:"enabled"`
	LastError       string   `json:"last_error,omitempty"`
	Logs            []string `json:"logs,omitempty"`
	Previous        *Saved   `json:"previous,omitempty"`
	StorageBytes    uint64   `json:"storage_bytes,omitempty"`
	PreviousBytes   uint64   `json:"previous_bytes,omitempty"`
	WASMMemoryBytes uint32   `json:"wasm_memory_bytes,omitempty"`
	Update          *Entry   `json:"update,omitempty"`
}
type Settings struct {
	CatalogURL string `json:"catalog_url"`
	CatalogKey string `json:"catalog_key"`
	Revision   uint64 `json:"revision"`
}
type State struct {
	Plugins  map[string]*Installed `json:"plugins"`
	Settings Settings              `json:"settings"`
	Revoked  []string              `json:"revoked,omitempty"`
	Sources  []Source              `json:"sources,omitempty"`
}
type ticket struct {
	Package  *Package
	Reviewed bool
	Expires  time.Time
	SourceID string
}

// catalogRefreshInterval is how often source catalogs and yam registries are
// fetched in the background.
const catalogRefreshInterval = 3 * time.Hour

type Manager struct {
	mu              sync.Mutex
	dir             string
	state           State
	running         map[string]PluginRunner
	tickets         map[string]ticket
	Ring            *Ring
	Audio           *AudioMeter
	catalog         *Catalog
	CatalogError    string
	AllowLocalStore bool
	catalogs        map[string]*Catalog
	diskProbe       func(string) DiskUsage
	Updater         *Updater
	Device          DeviceProfile
	Yam             *Yam
	ready           bool
}

func NewManager(dir string, hardware, allowLocal bool) (*Manager, error) {
	if e := os.MkdirAll(filepath.Join(dir, "packages"), 0700); e != nil {
		return nil, e
	}
	ring, e := NewRing(hardware)
	if e != nil {
		return nil, e
	}
	m := &Manager{dir: dir, Ring: ring, running: map[string]PluginRunner{}, tickets: map[string]ticket{}, AllowLocalStore: allowLocal, catalogs: map[string]*Catalog{}, diskProbe: diskUsage}
	m.Audio = &AudioMeter{}
	m.state.Plugins = map[string]*Installed{}
	b, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if e == nil {
		if e = decode(b, &m.state); e != nil {
			ring.Close()
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		ring.Close()
		return nil, e
	}
	if m.state.Plugins == nil {
		m.state.Plugins = map[string]*Installed{}
	}
	for id, p := range m.state.Plugins {
		if p == nil || id != p.Manifest.ID || ValidateManifest(p.Manifest) != nil || !validHash(p.SHA256) {
			ring.Close()
			return nil, errors.New("invalid installed plugin record")
		}
		p.Logs = boundedLogLines(p.Logs)
	}
	if e = m.initSources(); e != nil {
		ring.Close()
		return nil, e
	}
	m.gc()
	return m, nil
}
func (m *Manager) save() error { return atomicJSON(filepath.Join(m.dir, "state.json"), m.state) }
func (m *Manager) load(hash string) (*Package, error) {
	if !validHash(hash) {
		return nil, errors.New("invalid hash")
	}
	b, e := os.ReadFile(filepath.Join(m.dir, "packages", hash+".aliceplugin"))
	if e != nil {
		return nil, e
	}
	p, e := Parse(b)
	if e != nil {
		return nil, e
	}
	if p.SHA256 != hash {
		return nil, errors.New("installed package checksum mismatch")
	}
	return p, nil
}
func (m *Manager) Stage(raw []byte, reviewed bool) (map[string]any, error) {
	p, e := Parse(raw)
	if e != nil {
		return nil, e
	}
	if e = ValidateModule(p); e != nil {
		return nil, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.tickets {
		if time.Now().After(t.Expires) {
			delete(m.tickets, id)
		}
	}
	if len(m.tickets) >= 4 {
		return nil, errors.New("too many pending package confirmations")
	}
	id := randomID()
	m.tickets[id] = ticket{Package: p, Reviewed: reviewed, Expires: time.Now().Add(10 * time.Minute)}
	blocking, note := m.Device.Compatibility(p.Manifest)
	return map[string]any{"ticket": id, "manifest": p.Manifest, "sha256": p.SHA256, "bytes": len(raw), "reviewed": reviewed, "storage": m.storagePlanLocked(p.RequiredStorage()), "incompatible": blocking, "compatibility_note": note}, nil
}
func (m *Manager) Install(id string, permissions []string, acknowledgeLocal bool) error {
	return m.install(id, permissions, acknowledgeLocal, "", "", false)
}
func (m *Manager) install(id string, permissions []string, acknowledgeLocal bool, updateID, expectedSHA string, acknowledgeUpdate bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok || time.Now().After(t.Expires) {
		return errors.New("package confirmation expired")
	}
	p := t.Package
	old := m.state.Plugins[p.Manifest.ID]
	updating := updateID != ""
	if updating {
		if old == nil || p.Manifest.ID != updateID || old.SHA256 != expectedSHA {
			return errors.New("plugin changed; refresh before updating")
		}
		if !old.Reviewed || !t.Reviewed || old.SourceID == "" || old.SourceID != t.SourceID {
			return errors.New("plugin updates must come from the original trusted source")
		}
		if !newerPluginVersion(p.Manifest.Version, old.Manifest.Version) {
			return errors.New("a newer plugin version is required")
		}
		if expandedPluginAccess(old, p.Manifest) {
			if !acknowledgeUpdate {
				return errors.New("confirm newly requested plugin permissions")
			}
		} else {
			permissions = p.Manifest.Permissions
		}
	}
	if reason, _ := m.Device.Compatibility(p.Manifest); reason != "" {
		return errors.New("plugin is not compatible with " + m.Device.ID + ": " + reason)
	}
	if !accepted(p.Manifest, permissions) {
		return errors.New("confirm every requested permission")
	}
	if !t.Reviewed && !acknowledgeLocal {
		return errors.New("this local package has not been reviewed by the store")
	}
	if t.Reviewed {
		found := false
		if m.catalog != nil {
			for _, entry := range m.catalog.Entries {
				if entry.SHA256 == p.SHA256 && (t.SourceID == "" || t.SourceID == entry.SourceID) {
					c := m.catalogs[entry.SourceID]
					if c == nil || !time.Now().Before(c.Expires) {
						continue
					}
					t.SourceID = entry.SourceID
					found = true
				}
			}
		}
		if !found {
			return errors.New("version is no longer in the current approved catalog")
		}
	}
	if m.savedRevoked(Saved{SHA256: p.SHA256, SourceID: t.SourceID}) {
		return errors.New("version revoked by the moderator")
	}
	if old == nil && len(m.state.Plugins) >= 8 {
		return errors.New("maximum 8 installed plugins")
	}
	if old != nil && old.SHA256 == p.SHA256 {
		return errors.New("this version is already installed")
	}
	if plan := m.storagePlanLocked(p.RequiredStorage()); !plan.Fits {
		return &StorageError{Plan: plan}
	}
	supplied := map[string]any{}
	if old != nil {
		for _, f := range p.Manifest.Config {
			if v, ok := old.Config[f.Key]; ok {
				supplied[f.Key] = v
			}
		}
	}
	config, e := Config(p.Manifest, supplied)
	if e != nil {
		return fmt.Errorf("configuration migration: %w", e)
	}
	if e = putPackage(filepath.Join(m.dir, "packages"), p); e != nil {
		return e
	}
	current := &Installed{Saved: Saved{Manifest: p.Manifest, SHA256: p.SHA256, Config: config, Reviewed: t.Reviewed, SourceID: t.SourceID}}
	current.NativeConsent = p.Manifest.Engine == "native" && Has(p.Manifest, "system.full-access") && accepted(p.Manifest, permissions)
	if source := m.sourceLocked(t.SourceID); source != nil {
		current.SourceName = source.Name
		current.SourceOfficial = source.Official
	}
	if old != nil {
		prior := clone(old.Saved)
		current.Previous = &prior
	}
	if updating {
		current.Enabled = old.Enabled
	}
	m.state.Plugins[p.Manifest.ID] = current
	if e = m.save(); e != nil {
		if old == nil {
			delete(m.state.Plugins, p.Manifest.ID)
		} else {
			m.state.Plugins[p.Manifest.ID] = old
		}
		return e
	}
	m.stop(p.Manifest.ID)
	if old != nil && Has(old.Manifest, "led.ring") {
		m.Ring.Publish(make([]byte, 36))
	}
	if updating && current.Enabled {
		if startErr := m.start(p.Manifest.ID); startErr != nil {
			m.state.Plugins[p.Manifest.ID] = old
			if restartErr := m.start(p.Manifest.ID); restartErr != nil {
				old.Enabled = false
				old.LastError = fmt.Sprintf("update failed: %v; previous version could not restart: %v", startErr, restartErr)
			}
			saveErr := m.save()
			delete(m.tickets, id)
			m.gc()
			if saveErr != nil {
				return fmt.Errorf("plugin update failed: %v; saving rollback failed: %w", startErr, saveErr)
			}
			return fmt.Errorf("plugin update failed; restored previous version: %w", startErr)
		}
	}
	delete(m.tickets, id)
	m.gc()
	return nil
}
func (m *Manager) start(id string) error {
	p := m.state.Plugins[id]
	if p == nil {
		return errors.New("plugin not installed")
	}
	if p.Manifest.Engine == "native" && !p.NativeConsent {
		return errors.New("native full-access consent is required")
	}
	if m.savedRevoked(p.Saved) {
		return errors.New("version revoked by the moderator")
	}
	if _, ok := m.running[id]; ok {
		return nil
	}
	if len(m.running) >= 4 {
		return errors.New("maximum 4 active plugins")
	}
	if Has(p.Manifest, "led.ring") {
		for other := range m.running {
			if other != id && Has(m.state.Plugins[other].Manifest, "led.ring") {
				return errors.New("another plugin already controls the LED ring")
			}
		}
	}
	pkg, e := m.load(p.SHA256)
	if e != nil {
		return e
	}
	config, e := Config(pkg.Manifest, p.Config)
	if e != nil {
		return e
	}
	var r PluginRunner
	if pkg.Manifest.Engine == "native" {
		r, e = NewNativeRunner(pkg, config, m.dir)
	} else {
		r, e = NewRunner(pkg, config)
	}
	if e != nil {
		return e
	}
	m.running[id] = r
	p.LastError = ""
	p.Logs = nil
	return nil
}
func (m *Manager) stop(id string) {
	if r, ok := m.running[id]; ok {
		if p := m.state.Plugins[id]; p != nil && Has(p.Manifest, "led.ring") {
			m.Ring.Publish(make([]byte, 36))
		}
		r.Close()
		if p := m.state.Plugins[id]; p != nil {
			p.Logs = boundedLogLines(r.LogLines())
		}
		delete(m.running, id)
	}
}
func (m *Manager) Enable(id string, enable bool) error {
	return m.EnableWithConsent(id, enable, false)
}
func (m *Manager) EnableWithConsent(id string, enable, acknowledgeNative bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.state.Plugins[id]
	if p == nil {
		return errors.New("plugin not installed")
	}
	if enable {
		if p.Manifest.Engine == "native" && !acknowledgeNative {
			return errors.New("confirm native full-root access before enabling")
		}
		if e := m.start(id); e != nil {
			return e
		}
	}
	old := p.Enabled
	p.Enabled = enable
	if e := m.save(); e != nil {
		p.Enabled = old
		if enable && !old {
			m.stop(id)
		}
		return e
	}
	if !enable {
		m.stop(id)
		return m.save()
	}
	return nil
}
func (m *Manager) SetConfig(id string, values map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.state.Plugins[id]
	if p == nil {
		return errors.New("plugin not installed")
	}
	next, e := Config(p.Manifest, values)
	if e != nil {
		return e
	}
	old := p.Config
	p.Config = next
	if e = m.save(); e != nil {
		p.Config = old
	}
	if e == nil && p.Enabled && p.Manifest.Engine == "native" {
		m.stop(id)
		if e = m.start(id); e != nil {
			p.Enabled = false
			p.LastError = e.Error()
			m.save()
		}
	}
	return e
}
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.state.Plugins[id]
	if old == nil {
		return errors.New("plugin not installed")
	}
	delete(m.state.Plugins, id)
	if e := m.save(); e != nil {
		m.state.Plugins[id] = old
		return e
	}
	m.stop(id)
	m.gc()
	dataRoot, e := filepath.Abs(filepath.Join(m.dir, "plugin-data"))
	if e != nil {
		return e
	}
	target := filepath.Join(dataRoot, id)
	relative, e := filepath.Rel(dataRoot, target)
	if e != nil || relative != id || !idRE.MatchString(id) {
		return errors.New("invalid plugin data path")
	}
	if e = os.RemoveAll(target); e != nil {
		return fmt.Errorf("plugin removed, but data cleanup failed: %w", e)
	}
	return nil
}
func (m *Manager) Rollback(id string, permissions []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.state.Plugins[id]
	if old == nil || old.Previous == nil {
		return errors.New("no previous version")
	}
	prev := clone(*old.Previous)
	if m.savedRevoked(prev) {
		return errors.New("version revoked by the moderator")
	}
	if !accepted(prev.Manifest, permissions) {
		return errors.New("confirm previous version permissions")
	}
	pkg, e := m.load(prev.SHA256)
	if e != nil {
		return e
	}
	if e = ValidateModule(pkg); e != nil {
		return e
	}
	m.state.Plugins[id] = &Installed{Saved: prev}
	if e = m.save(); e != nil {
		m.state.Plugins[id] = old
		return e
	}
	m.stop(id)
	m.gc()
	return nil
}
func (m *Manager) gc() {
	used := map[string]bool{}
	for _, p := range m.state.Plugins {
		used[p.SHA256] = true
		if p.Previous != nil {
			used[p.Previous.SHA256] = true
		}
		// Keep the rollback archive, but regenerate its runtime only if needed.
		// Cleanup is best-effort; a read-only cache must not stop a plugin.
		_ = cleanRuntimeCache(filepath.Join(m.dir, "plugin-data"), p.Manifest.ID, p.SHA256)
	}
	files, _ := os.ReadDir(filepath.Join(m.dir, "packages"))
	for _, f := range files {
		name := f.Name()
		if len(name) == 76 && filepath.Ext(name) == ".aliceplugin" {
			hash := name[:64]
			if validHash(hash) && !used[hash] {
				os.Remove(filepath.Join(m.dir, "packages", name))
			}
		}
	}
}
func (m *Manager) revoked(hash string) bool {
	for _, v := range m.state.Revoked {
		if v == hash {
			return true
		}
	}
	return false
}
func (m *Manager) Snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	plugins := []*Installed{}
	for _, p := range m.state.Plugins {
		copy := clone(p)
		copy.Update = m.pluginUpdateLocked(p)
		copy.StorageBytes, copy.PreviousBytes = m.pluginBytesLocked(p)
		if r := m.running[p.Manifest.ID]; r != nil {
			copy.Logs = r.LogLines()
			copy.WASMMemoryBytes = r.MemoryBytes()
		}
		plugins = append(plugins, copy)
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].Manifest.ID < plugins[j].Manifest.ID })
	var catalog any
	if m.catalog != nil {
		catalog = clone(*m.catalog)
	}
	sources := []Source{}
	for _, s := range m.state.Sources {
		if !s.Removed {
			sources = append(sources, clone(s))
		}
	}
	return map[string]any{"updates": m.updateStatus(), "plugins": plugins, "settings": m.state.Settings, "sources": sources, "catalog": catalog, "catalog_error": m.CatalogError, "ring": m.Ring.Snapshot(), "system": m.systemLocked(), "device": m.Device, "free_bytes": m.diskProbe(m.dir).AvailableBytes, "version": Version}
}
func (m *Manager) Run(ctx context.Context) {
	go m.Audio.Serve(ctx)
	m.mu.Lock()
	for id, p := range m.state.Plugins {
		if p.Enabled {
			if e := m.start(id); e != nil {
				p.Enabled = false
				p.LastError = e.Error()
			}
		}
	}
	m.save()
	m.ready = true
	m.mu.Unlock()
	go func() { _ = m.RefreshAll(ctx) }()
	if m.Yam != nil {
		go func() {
			m.Yam.Refresh(ctx)
			m.Yam.AutoSetup(ctx, filepath.Join(m.dir, "yam-autosetup.json"))
		}()
	}
	// New plugins and yam packages show up without pressing Refresh.
	go func() {
		t := time.NewTicker(catalogRefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = m.RefreshAll(ctx)
				if m.Yam != nil {
					m.Yam.Refresh(ctx)
				}
			}
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	boot := time.Now()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for id := range m.running {
				m.stop(id)
			}
			_ = m.save()
			m.mu.Unlock()
			m.Ring.Flush()
			m.Ring.Close()
			return
		case <-ticker.C:
			m.mu.Lock()
			for id, r := range m.running {
				p := m.state.Plugins[id]
				frame, e := r.Tick(p.Config, uint32(time.Since(boot).Milliseconds()))
				if e != nil {
					p.Enabled = false
					p.LastError = e.Error()
					m.stop(id)
					m.save()
				} else if len(frame) == 36 {
					m.Ring.Publish(frame)
				}
			}
			m.mu.Unlock()
			m.Ring.Flush()
		}
	}
}

func (m *Manager) updateStatus() any {
	if m.Updater == nil {
		return nil
	}
	return m.Updater.Status()
}
