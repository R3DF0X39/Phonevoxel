package geo

import "math"

// CameraIntrinsics holds the pinhole camera model parameters.
type CameraIntrinsics struct {
	Width  int
	Height int
	Fx     float64 // focal length in pixels (horizontal)
	Fy     float64 // focal length in pixels (vertical)
	Cx     float64 // principal point x
	Cy     float64 // principal point y
}

// IntrinsicsFromHFOV computes camera intrinsics from reported HFOV and frame size.
// Square pixels are assumed (Fx == Fy).
func IntrinsicsFromHFOV(width, height int, hfovDegrees float64) CameraIntrinsics {
	if hfovDegrees <= 0 || hfovDegrees >= 180 {
		hfovDegrees = 70.0
	}
	hfov := hfovDegrees * math.Pi / 180.0
	fx := float64(width) / (2.0 * math.Tan(hfov/2.0))
	return CameraIntrinsics{
		Width: width,
		Height: height,
		Fx:    fx,
		Fy:    fx,
		Cx:    float64(width) / 2.0,
		Cy:    float64(height) / 2.0,
	}
}

// CameraPose is the full 6-DOF pose of the camera in the ENU frame.
type CameraPose struct {
	// Position in ENU meters
	East, North, Up float64

	// Rotation matrix: transforms ENU world vectors to camera/device space.
	// v_camera = R * v_enu
	R Mat3
}

// ComputePose builds a CameraPose from GPS + W3C device orientation angles,
// using the provided geo converter for the coordinate transform.
func ComputePose(conv *Converter, lat, lon, alt, alpha, beta, gamma float64) CameraPose {
	east, north, up := conv.GPSToENU(lat, lon, alt)
	r := OrientationToRotationMatrix(alpha, beta, gamma)
	return CameraPose{
		East: east, North: north, Up: up,
		R: r,
	}
}

// PixelToRayENU returns a unit direction vector in ENU world space for the
// camera ray passing through pixel (px, py).
//
// Steps:
//  1. Normalise pixel to camera-space direction (using intrinsics).
//  2. Rotate from camera space to ENU using the transpose of the pose rotation.
func PixelToRayENU(px, py float64, intr CameraIntrinsics, pose CameraPose) [3]float64 {
	// Camera-space direction (Z forward, X right, Y down convention)
	dx := (px - intr.Cx) / intr.Fx
	dy := (py - intr.Cy) / intr.Fy
	dz := 1.0

	// Normalise
	mag := math.Sqrt(dx*dx + dy*dy + dz*dz)
	camDir := [3]float64{dx / mag, dy / mag, dz / mag}

	// Rotate camera-space dir to ENU world space.
	// pose.R maps world→camera, so its transpose maps camera→world.
	worldDir := pose.R.Transpose().MulVec3(camDir)
	return normalize3(worldDir)
}

// ComputePoseFromAzimuthElevation builds a CameraPose for a fixed-mount camera
// (e.g. a webcam on a tripod) without using W3C device-orientation angles.
//
//   az_deg   – compass azimuth of the viewing direction, degrees clockwise from north (0–360)
//   el_deg   – elevation angle above horizontal, degrees  (-90 = straight down, +90 = straight up)
//   roll_deg – rotation around the viewing axis, degrees clockwise when viewed from behind the camera
func ComputePoseFromAzimuthElevation(conv *Converter, lat, lon, alt, az_deg, el_deg, roll_deg float64) CameraPose {
	east, north, up := conv.GPSToENU(lat, lon, alt)

	az := az_deg * math.Pi / 180.0
	el := el_deg * math.Pi / 180.0

	cosEl := math.Cos(el)

	// Forward direction in ENU (East=idx0, North=idx1, Up=idx2)
	fwd := [3]float64{
		math.Sin(az) * cosEl,
		math.Cos(az) * cosEl,
		math.Sin(el),
	}

	// Right = cross(fwd, worldUp), normalized.
	// When nearly vertical use south as the reference to avoid degeneracy.
	worldUp := [3]float64{0, 0, 1}
	var right [3]float64
	if math.Abs(el_deg) > 89.0 {
		southRef := [3]float64{0, -1, 0}
		right = normalize3(cross3(southRef, fwd))
	} else {
		right = normalize3(cross3(fwd, worldUp))
	}

	// Down = cross(fwd, right)  →  satisfies right × down = fwd  (camera X×Y=Z)
	down := normalize3(cross3(fwd, right))

	// Apply roll: rotate right and down around the forward axis.
	if roll_deg != 0 {
		roll := roll_deg * math.Pi / 180.0
		cosR, sinR := math.Cos(roll), math.Sin(roll)
		newRight := [3]float64{
			right[0]*cosR + down[0]*sinR,
			right[1]*cosR + down[1]*sinR,
			right[2]*cosR + down[2]*sinR,
		}
		down = [3]float64{
			down[0]*cosR - right[0]*sinR,
			down[1]*cosR - right[1]*sinR,
			down[2]*cosR - right[2]*sinR,
		}
		right = newRight
	}

	// Rotation matrix rows = [right, down, fwd] → maps ENU world → camera space.
	r := Mat3{
		{right[0], right[1], right[2]},
		{down[0], down[1], down[2]},
		{fwd[0], fwd[1], fwd[2]},
	}
	return CameraPose{East: east, North: north, Up: up, R: r}
}

func cross3(a, b [3]float64) [3]float64 {
	return [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

func normalize3(v [3]float64) [3]float64 {
	mag := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if mag < 1e-12 {
		return [3]float64{0, 0, 1}
	}
	return [3]float64{v[0] / mag, v[1] / mag, v[2] / mag}
}
