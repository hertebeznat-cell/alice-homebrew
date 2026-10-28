package homebrew

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

const StorageReserve = uint64(8 << 20)

type DiskUsage struct {
	Available            bool   `json:"available"`
	TotalBytes           uint64 `json:"total_bytes"`
	UsedBytes            uint64 `json:"used_bytes"`
	AvailableBytes       uint64 `json:"available_bytes"`
	ReserveBytes         uint64 `json:"reserve_bytes"`
	PluginAvailableBytes uint64 `json:"plugin_available_bytes"`
}
type MemoryUsage struct {
	Available      bool   `json:"available"`
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	ProcessBytes   uint64 `json:"process_bytes"`
	GoHeapBytes    uint64 `json:"go_heap_bytes"`
}
type PluginUsage struct {
	ID            string `json:"id"`
	Title         Text   `json:"title"`
	Bytes         uint64 `json:"bytes"`
	PreviousBytes uint64 `json:"previous_bytes"`
	Enabled       bool   `json:"enabled"`
}
type StoragePlan struct {
	Fits           bool          `json:"fits"`
	AvailableBytes uint64        `json:"available_bytes"`
	PackageBytes   uint64        `json:"package_bytes"`
	RequiredBytes  uint64        `json:"required_bytes"`
	ReserveBytes   uint64        `json:"reserve_bytes"`
	MissingBytes   uint64        `json:"missing_bytes"`
	Candidates     []PluginUsage `json:"candidates"`
}
type StorageError struct{ Plan StoragePlan }

func (e *StorageError) Error() string {
	return "not enough storage; select plugins to remove and retry"
}
func fileBytes(path string) uint64 {
	s, e := os.Stat(path)
	if e != nil || !s.Mode().IsRegular() {
		return 0
	}
	return uint64(s.Size())
}
func (m *Manager) pluginBytesLocked(p *Installed) (uint64, uint64) {
	current := fileBytes(filepath.Join(m.dir, "packages", p.SHA256+".aliceplugin"))
	var previous uint64
	if p.Previous != nil && p.Previous.SHA256 != p.SHA256 {
		previous = fileBytes(filepath.Join(m.dir, "packages", p.Previous.SHA256+".aliceplugin"))
	}
	current += directoryBytes(filepath.Join(m.dir, "plugin-data", p.Manifest.ID))
	return current + previous, previous
}
func directoryBytes(root string) uint64 {
	var total uint64
	filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e == nil && d.Type().IsRegular() {
			if s, e := d.Info(); e == nil {
				total += uint64(s.Size())
			}
		}
		return nil
	})
	return total
}
func (m *Manager) storagePlanLocked(packageBytes uint64) StoragePlan {
	d := m.diskProbe(m.dir)
	plan := StoragePlan{AvailableBytes: d.AvailableBytes, PackageBytes: packageBytes, RequiredBytes: packageBytes + StorageReserve, ReserveBytes: StorageReserve, Candidates: []PluginUsage{}}
	plan.Fits = d.Available && plan.AvailableBytes >= plan.RequiredBytes
	if !plan.Fits && plan.RequiredBytes > plan.AvailableBytes {
		plan.MissingBytes = plan.RequiredBytes - plan.AvailableBytes
	}
	for id, p := range m.state.Plugins {
		size, previous := m.pluginBytesLocked(p)
		plan.Candidates = append(plan.Candidates, PluginUsage{ID: id, Title: p.Manifest.Title, Bytes: size, PreviousBytes: previous, Enabled: p.Enabled})
	}
	sort.Slice(plan.Candidates, func(i, j int) bool {
		a, b := plan.Candidates[i], plan.Candidates[j]
		if a.Bytes == b.Bytes {
			return a.ID < b.ID
		}
		return a.Bytes > b.Bytes
	})
	return plan
}
func (m *Manager) TicketStorage(id string) (StoragePlan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tickets[id]
	if !ok || time.Now().After(t.Expires) {
		return StoragePlan{}, errors.New("package confirmation expired")
	}
	return m.storagePlanLocked(t.Package.RequiredStorage()), nil
}
func (m *Manager) systemLocked() map[string]any {
	d := m.diskProbe(m.dir)
	d.ReserveBytes = StorageReserve
	if d.AvailableBytes > StorageReserve {
		d.PluginAvailableBytes = d.AvailableBytes - StorageReserve
	}
	memory := memoryUsage()
	var heap runtime.MemStats
	runtime.ReadMemStats(&heap)
	memory.GoHeapBytes = heap.HeapAlloc
	return map[string]any{"disk": d, "memory": memory, "version": Version, "os": runtime.GOOS, "architecture": runtime.GOARCH, "cpu_count": runtime.NumCPU(), "plugin_count": len(m.state.Plugins), "running_count": len(m.running), "max_plugins": 8, "max_running": 4, "hardware_led": m.Ring.Snapshot()["hardware"], "device": m.Device}
}
func (m *Manager) System() map[string]any { m.mu.Lock(); defer m.mu.Unlock(); return m.systemLocked() }
func (m *Manager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	plugins := []map[string]any{}
	for id, p := range m.state.Plugins {
		var memory uint32
		logs := p.Logs
		if r := m.running[id]; r != nil {
			memory = r.MemoryBytes()
			logs = r.LogLines()
		}
		plugins = append(plugins, map[string]any{"id": id, "enabled": p.Enabled, "last_error": p.LastError, "wasm_memory_bytes": memory, "logs": boundedLogLines(logs)})
	}
	status := map[string]any{"plugins": plugins, "system": m.systemLocked(), "updates": m.updateStatus()}
	if m.Yam != nil {
		status["yam"] = m.Yam.Jobs()
	}
	return status
}
