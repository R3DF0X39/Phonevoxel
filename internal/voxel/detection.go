package voxel

import "github.com/phonevoxel/internal/types"

// DetectTargets finds local maxima above threshold and clusters adjacent
// hot voxels into single targets. Returns one Target per cluster.
func (g *Grid) DetectTargets(threshold float64) []types.Target {
	g.mu.RLock()
	defer g.mu.RUnlock()

	type cell struct{ ix, iy, iz int }
	visited := make(map[cell]bool)
	var targets []types.Target

	for iz := 0; iz < g.nz; iz++ {
		for iy := 0; iy < g.ny; iy++ {
			for ix := 0; ix < g.nx; ix++ {
				c := cell{ix, iy, iz}
				if visited[c] {
					continue
				}
				v := g.data[g.index(ix, iy, iz)]
				if v < threshold {
					continue
				}

				// BFS flood-fill to collect adjacent above-threshold voxels
				queue := []cell{c}
				visited[c] = true
				var sumE, sumN, sumU, sumV float64
				var count int

				for len(queue) > 0 {
					cur := queue[0]
					queue = queue[1:]
					cv := g.data[g.index(cur.ix, cur.iy, cur.iz)]
					ce := g.cfg.MinE + (float64(cur.ix)+0.5)*g.cfg.Resolution
					cn := g.cfg.MinN + (float64(cur.iy)+0.5)*g.cfg.Resolution
					cu := g.cfg.MinU + (float64(cur.iz)+0.5)*g.cfg.Resolution
					sumE += ce * cv
					sumN += cn * cv
					sumU += cu * cv
					sumV += cv
					count++

					// Check 6-connected neighbours
					for _, d := range [][3]int{
						{1, 0, 0}, {-1, 0, 0},
						{0, 1, 0}, {0, -1, 0},
						{0, 0, 1}, {0, 0, -1},
					} {
						ni := cell{cur.ix + d[0], cur.iy + d[1], cur.iz + d[2]}
						if ni.ix < 0 || ni.ix >= g.nx ||
							ni.iy < 0 || ni.iy >= g.ny ||
							ni.iz < 0 || ni.iz >= g.nz {
							continue
						}
						if visited[ni] {
							continue
						}
						nv := g.data[g.index(ni.ix, ni.iy, ni.iz)]
						if nv >= threshold {
							visited[ni] = true
							queue = append(queue, ni)
						}
					}
				}

				if count == 0 || sumV == 0 {
					continue
				}
				// Weighted centroid
				centroidE := sumE / sumV
				centroidN := sumN / sumV
				centroidU := sumU / sumV
				confidence := min64(sumV/float64(count)/1.0, 1.0)

				targets = append(targets, types.Target{
					Position:   [3]float64{centroidE, centroidN, centroidU},
					Confidence: confidence,
				})
			}
		}
	}
	return targets
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
