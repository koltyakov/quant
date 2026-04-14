package index

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
)

type DiskBackedHNSW struct {
	inner        *hnswIndex
	graphPath    string
	flushThresh  int
	mu           sync.Mutex
	pendingAdds  int64
	hnswM        int
	hnswEfSearch int
}

func NewDiskBackedHNSW(graphPath string, m, efSearch, flushThreshold int) *DiskBackedHNSW {
	if flushThreshold <= 0 {
		flushThreshold = 5000
	}
	return &DiskBackedHNSW{
		inner:        newHNSWIndex(),
		graphPath:    graphPath,
		flushThresh:  flushThreshold,
		hnswM:        m,
		hnswEfSearch: efSearch,
	}
}

func (d *DiskBackedHNSW) Add(id int, vec []float32) {
	d.inner.Add(id, vec)
	d.mu.Lock()
	d.pendingAdds++
	flush := d.pendingAdds >= int64(d.flushThresh)
	if flush {
		d.pendingAdds = 0
	}
	d.mu.Unlock()
	if flush {
		d.flushToDisk()
	}
}

func (d *DiskBackedHNSW) Delete(id int) {
	d.inner.Delete(id)
}

func (d *DiskBackedHNSW) BatchDelete(ids []int) {
	d.inner.BatchDelete(ids)
}

func (d *DiskBackedHNSW) BatchAdd(nodes []hnsw.Node[int]) {
	d.inner.BatchAdd(nodes)
	d.mu.Lock()
	d.pendingAdds += int64(len(nodes))
	flush := d.pendingAdds >= int64(d.flushThresh)
	if flush {
		d.pendingAdds = 0
	}
	d.mu.Unlock()
	if flush {
		d.flushToDisk()
	}
}

func (d *DiskBackedHNSW) Search(query []float32, k int) []int {
	return d.inner.Search(query, k)
}

func (d *DiskBackedHNSW) Len() int {
	return d.inner.Len()
}

func (d *DiskBackedHNSW) Ready() bool {
	return d.inner.ready.Load()
}

func (d *DiskBackedHNSW) SetGraph(graph *hnsw.Graph[int]) {
	d.inner.mu.Lock()
	d.inner.graph = graph
	d.inner.mu.Unlock()
	d.inner.ready.Store(true)
	d.inner.resetMods()
}

func (d *DiskBackedHNSW) flushToDisk() {
	if d.graphPath == "" || !d.inner.ready.Load() {
		return
	}

	d.inner.mu.RLock()
	g := d.inner.graph
	d.inner.mu.RUnlock()

	if g == nil {
		return
	}

	f, err := os.CreateTemp(filepath.Dir(d.graphPath), ".quant-hnsw-disk-*")
	if err != nil {
		logx.Warn("disk-backed hnsw flush: temp file creation failed", "err", err)
		return
	}
	tmpPath := f.Name()

	if err := g.Export(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		logx.Warn("disk-backed hnsw flush: export failed", "err", err)
		return
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}

	if err := os.Rename(tmpPath, d.graphPath); err != nil {
		_ = os.Remove(tmpPath)
		logx.Warn("disk-backed hnsw flush: rename failed", "err", err)
	}
}

func (d *DiskBackedHNSW) LoadFromDisk(ctx context.Context) error {
	if d.graphPath == "" {
		return nil
	}

	info, err := os.Stat(d.graphPath)
	if err != nil || info.Size() == 0 {
		return nil
	}

	f, err := os.Open(d.graphPath)
	if err != nil {
		return fmt.Errorf("opening hnsw graph file: %w", err)
	}
	defer func() { _ = f.Close() }()

	g := newGraph(d.hnswM, d.hnswEfSearch)
	if err := g.Import(bufio.NewReader(f)); err != nil {
		return fmt.Errorf("importing hnsw graph: %w", err)
	}

	d.inner.mu.Lock()
	d.inner.graph = g
	d.inner.mu.Unlock()
	d.inner.ready.Store(true)
	d.inner.resetMods()
	d.mu.Lock()
	d.pendingAdds = 0
	d.mu.Unlock()
	logx.Info("disk-backed hnsw graph loaded from file", "path", d.graphPath, "nodes", g.Len())
	return nil
}

func (d *DiskBackedHNSW) ModCount() int64 {
	return d.inner.modCount()
}
