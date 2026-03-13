package voxel

import "math"

// Ray represents a 3D ray in ENU space.
type Ray struct {
	// Origin in ENU meters
	OriginE, OriginN, OriginU float64
	// Unit direction vector
	DirE, DirN, DirU float64
}

// NewRay creates a Ray and normalises the direction vector.
func NewRay(originE, originN, originU, dirE, dirN, dirU float64) Ray {
	mag := math.Sqrt(dirE*dirE + dirN*dirN + dirU*dirU)
	if mag < 1e-12 {
		mag = 1
	}
	return Ray{
		OriginE: originE, OriginN: originN, OriginU: originU,
		DirE: dirE / mag, DirN: dirN / mag, DirU: dirU / mag,
	}
}

// marchCallback is called for each voxel cell the ray passes through.
// ix, iy, iz are grid indices, t is the parametric distance along the ray.
// Returning false stops the march early.
type marchCallback func(ix, iy, iz int, t float64) bool

// marchDDA steps through all voxels intersected by r using the 3D DDA algorithm
// (Amanatides & Woo, 1987). Only visits cells within [0,nx)×[0,ny)×[0,nz).
func marchDDA(r Ray, minE, minN, minU, res float64, nx, ny, nz int, cb marchCallback) {
	// Convert ray origin to grid space
	ox := (r.OriginE - minE) / res
	oy := (r.OriginN - minN) / res
	oz := (r.OriginU - minU) / res

	dx := r.DirE / res
	dy := r.DirN / res
	dz := r.DirU / res

	// Current voxel index
	ix := int(math.Floor(ox))
	iy := int(math.Floor(oy))
	iz := int(math.Floor(oz))

	// Step direction per axis
	stepX, stepY, stepZ := 1, 1, 1
	if dx < 0 {
		stepX = -1
	}
	if dy < 0 {
		stepY = -1
	}
	if dz < 0 {
		stepZ = -1
	}

	// t-value at which the ray crosses into the next cell boundary, per axis
	var tMaxX, tMaxY, tMaxZ float64
	if math.Abs(dx) < 1e-12 {
		tMaxX = math.Inf(1)
	} else {
		nextX := float64(ix)
		if stepX > 0 {
			nextX = float64(ix + 1)
		}
		tMaxX = (nextX - ox) / dx
	}
	if math.Abs(dy) < 1e-12 {
		tMaxY = math.Inf(1)
	} else {
		nextY := float64(iy)
		if stepY > 0 {
			nextY = float64(iy + 1)
		}
		tMaxY = (nextY - oy) / dy
	}
	if math.Abs(dz) < 1e-12 {
		tMaxZ = math.Inf(1)
	} else {
		nextZ := float64(iz)
		if stepZ > 0 {
			nextZ = float64(iz + 1)
		}
		tMaxZ = (nextZ - oz) / dz
	}

	// Delta-t to traverse one full cell per axis
	tDeltaX, tDeltaY, tDeltaZ := math.Inf(1), math.Inf(1), math.Inf(1)
	if math.Abs(dx) > 1e-12 {
		tDeltaX = math.Abs(1.0 / dx)
	}
	if math.Abs(dy) > 1e-12 {
		tDeltaY = math.Abs(1.0 / dy)
	}
	if math.Abs(dz) > 1e-12 {
		tDeltaZ = math.Abs(1.0 / dz)
	}

	// Advance t to where the ray first enters the grid (may already be inside)
	t := 0.0

	// If ray starts outside, advance to the grid boundary
	tEntry := 0.0
	if ix < 0 || ix >= nx || iy < 0 || iy >= ny || iz < 0 || iz >= nz {
		// Compute entry t via slab method
		tEntry = gridEntryT(ox, oy, oz, dx, dy, dz, nx, ny, nz)
		if math.IsInf(tEntry, 1) || tEntry < 0 {
			return // ray misses grid entirely
		}
		// Advance to entry point
		t = tEntry
		ox2 := ox + dx*t
		oy2 := oy + dy*t
		oz2 := oz + dz*t
		ix = clamp(int(math.Floor(ox2)), 0, nx-1)
		iy = clamp(int(math.Floor(oy2)), 0, ny-1)
		iz = clamp(int(math.Floor(oz2)), 0, nz-1)
		// Recompute tMax from new position
		if stepX > 0 {
			tMaxX = (float64(ix+1) - ox) / dx
		} else {
			tMaxX = (float64(ix) - ox) / dx
		}
		if math.IsInf(tDeltaX, 1) {
			tMaxX = math.Inf(1)
		}
		if stepY > 0 {
			tMaxY = (float64(iy+1) - oy) / dy
		} else {
			tMaxY = (float64(iy) - oy) / dy
		}
		if math.IsInf(tDeltaY, 1) {
			tMaxY = math.Inf(1)
		}
		if stepZ > 0 {
			tMaxZ = (float64(iz+1) - oz) / dz
		} else {
			tMaxZ = (float64(iz) - oz) / dz
		}
		if math.IsInf(tDeltaZ, 1) {
			tMaxZ = math.Inf(1)
		}
	}

	// Maximum number of steps to prevent infinite loop
	maxSteps := (nx + ny + nz) * 3
	for step := 0; step < maxSteps; step++ {
		if ix < 0 || ix >= nx || iy < 0 || iy >= ny || iz < 0 || iz >= nz {
			break
		}
		if !cb(ix, iy, iz, t) {
			break
		}
		// Advance to next cell boundary
		if tMaxX < tMaxY {
			if tMaxX < tMaxZ {
				t = tMaxX
				tMaxX += tDeltaX
				ix += stepX
			} else {
				t = tMaxZ
				tMaxZ += tDeltaZ
				iz += stepZ
			}
		} else {
			if tMaxY < tMaxZ {
				t = tMaxY
				tMaxY += tDeltaY
				iy += stepY
			} else {
				t = tMaxZ
				tMaxZ += tDeltaZ
				iz += stepZ
			}
		}
	}
}

// gridEntryT computes the t at which the ray first enters the grid [0,nx)×[0,ny)×[0,nz).
// Returns +Inf if the ray misses the grid entirely.
func gridEntryT(ox, oy, oz, dx, dy, dz float64, nx, ny, nz int) float64 {
	tMin := 0.0
	tMax := math.Inf(1)

	// X slab
	if math.Abs(dx) < 1e-12 {
		if ox < 0 || ox >= float64(nx) {
			return math.Inf(1)
		}
	} else {
		t1 := (0 - ox) / dx
		t2 := (float64(nx) - ox) / dx
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tMin {
			tMin = t1
		}
		if t2 < tMax {
			tMax = t2
		}
	}

	// Y slab
	if math.Abs(dy) < 1e-12 {
		if oy < 0 || oy >= float64(ny) {
			return math.Inf(1)
		}
	} else {
		t1 := (0 - oy) / dy
		t2 := (float64(ny) - oy) / dy
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tMin {
			tMin = t1
		}
		if t2 < tMax {
			tMax = t2
		}
	}

	// Z slab
	if math.Abs(dz) < 1e-12 {
		if oz < 0 || oz >= float64(nz) {
			return math.Inf(1)
		}
	} else {
		t1 := (0 - oz) / dz
		t2 := (float64(nz) - oz) / dz
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tMin {
			tMin = t1
		}
		if t2 < tMax {
			tMax = t2
		}
	}

	if tMin > tMax || tMax < 0 {
		return math.Inf(1)
	}
	if tMin < 0 {
		return 0
	}
	return tMin
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
