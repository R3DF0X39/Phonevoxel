package voxel

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phonevoxel/internal/types"
)

// Config holds the grid bounds and behaviour parameters.
type Config struct {
	MinE, MaxE     float64
	MinN, MaxN     float64
	MinU, MaxU     float64
	Resolution     float64
	DecayFactor    float64
	DecayInterval  time.Duration
}

// Grid is a thread-safe 3D accumulation grid.
type Grid struct {
	mu     sync.RWMutex
	cfg    Config
	data   []float64

	nx, ny, nz int

	// Live-tunable parameters (atomic-friendly via mutex)
	decayFactor atomic.Value // float64 stored as interface{}
}

// NewGrid allocates a zero-filled Grid.
func NewGrid(cfg Config) *Grid {
	nx := int(math.Ceil((cfg.MaxE - cfg.MinE) / cfg.Resolution))
	ny := int(math.Ceil((cfg.MaxN - cfg.MinN) / cfg.Resolution))
	nz := int(math.Ceil((cfg.MaxU - cfg.MinU) / cfg.Resolution))
	if nx <= 0 { nx = 1 }
	if ny <= 0 { ny = 1 }
	if nz <= 0 { nz = 1 }

	g := &Grid{
		cfg:  cfg,
		data: make([]float64, nx*ny*nz),
		nx:   nx, ny: ny, nz: nz,
	}
	g.decayFactor.Store(cfg.DecayFactor)
	return g
}

// Dims returns the grid dimensions.
func (g *Grid) Dims() (nx, ny, nz int) { return g.nx, g.ny, g.nz }

// Config returns a copy of the grid configuration.
func (g *Grid) Config() Config { return g.cfg }

// SetDecayFactor updates the decay factor without rebuilding the grid.
func (g *Grid) SetDecayFactor(f float64) { g.decayFactor.Store(f) }

// index converts 3D grid indices to a flat array offset.
func (g *Grid) index(ix, iy, iz int) int {
	return iz*g.nx*g.ny + iy*g.nx + ix
}

// AccumulateRay adds motion magnitude to all voxels along a ray.
func (g *Grid) AccumulateRay(r Ray, magnitude float64) {
	if magnitude <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	marchDDA(r, g.cfg.MinE, g.cfg.MinN, g.cfg.MinU, g.cfg.Resolution,
		g.nx, g.ny, g.nz,
		func(ix, iy, iz int, _ float64) bool {
			idx := g.index(ix, iy, iz)
			g.data[idx] += magnitude
			return true
		})
}

// Decay multiplies every voxel by the decay factor.
func (g *Grid) Decay() {
	factor := g.decayFactor.Load().(float64)
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.data {
		g.data[i] *= factor
	}
}

// Reset zeroes the entire grid.
func (g *Grid) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.data {
		g.data[i] = 0
	}
}

// RunDecay runs Decay on the configured interval until ctx is cancelled.
func (g *Grid) RunDecay(ctx context.Context) {
	ticker := time.NewTicker(g.cfg.DecayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.Decay()
		}
	}
}

// SparseSnapshot returns all voxels above the given threshold.
func (g *Grid) SparseSnapshot(threshold float64) []types.SparseVoxel {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []types.SparseVoxel
	for iz := 0; iz < g.nz; iz++ {
		for iy := 0; iy < g.ny; iy++ {
			for ix := 0; ix < g.nx; ix++ {
				v := g.data[g.index(ix, iy, iz)]
				if v >= threshold {
					out = append(out, types.SparseVoxel{IX: ix, IY: iy, IZ: iz, Value: v})
				}
			}
		}
	}
	return out
}

// GridMeta returns dimension and bounds metadata for the dashboard.
func (g *Grid) GridMeta() types.GridMeta {
	return types.GridMeta{
		MinE: g.cfg.MinE, MaxE: g.cfg.MaxE,
		MinN: g.cfg.MinN, MaxN: g.cfg.MaxN,
		MinU: g.cfg.MinU, MaxU: g.cfg.MaxU,
		Resolution: g.cfg.Resolution,
		NX: g.nx, NY: g.ny, NZ: g.nz,
	}
}

// QueryRay returns the position and value of the highest-valued voxel along r.
func (g *Grid) QueryRay(r Ray) (posE, posN, posU, value float64) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	bestVal := -1.0
	bestIX, bestIY, bestIZ := 0, 0, 0

	marchDDA(r, g.cfg.MinE, g.cfg.MinN, g.cfg.MinU, g.cfg.Resolution,
		g.nx, g.ny, g.nz,
		func(ix, iy, iz int, _ float64) bool {
			v := g.data[g.index(ix, iy, iz)]
			if v > bestVal {
				bestVal = v
				bestIX, bestIY, bestIZ = ix, iy, iz
			}
			return true
		})

	if bestVal < 0 {
		return 0, 0, 0, 0
	}
	posE = g.cfg.MinE + (float64(bestIX)+0.5)*g.cfg.Resolution
	posN = g.cfg.MinN + (float64(bestIY)+0.5)*g.cfg.Resolution
	posU = g.cfg.MinU + (float64(bestIZ)+0.5)*g.cfg.Resolution
	return posE, posN, posU, bestVal
}
