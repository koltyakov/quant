package index

import (
	"context"
	"os"

	"github.com/coder/hnsw"
	"github.com/koltyakov/quant/internal/logx"
)

type DiskBackedHNSW struct {
	inner       *hnswIndex
	graphPath   string
	flushThresh int
	pendingAdds int64
}

func NewDiskBackedHNSW(graphPath string, m, efSearch, flushThreshold int) *DiskBackedHNSW {
	if flushThreshold <= 0 {
		flushThreshold = 5000
	}
	return &DiskBackedHNSW{
		inner:       newHNSWIndex(),
		graphPath:   graphPath,
		flushThresh: flushThreshold,
	}
}

func (d *DiskBackedHNSW) Add(id int, vec []float32) {
	d.inner.Add(id, vec)
	d.pendingAdds++
	if d.pendingAdds >= int64(d.flushThresh) {
		d.flushToDisk()
		d.pendingAdds = 0
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
	d.pendingAdds += int64(len(nodes))
	if d.pendingAdds >= int64(d.flushThresh) {
		d.flushToDisk()
		d.pendingAdds = 0
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

	f, err := os.CreateTemp(".", ".quant-hnsw-disk-*")
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

	return nil
}

func (d *DiskBackedHNSW) ModCount() int64 {
	return d.inner.modCount()
}
