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

func normalize3(v [3]float64) [3]float64 {
	mag := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if mag < 1e-12 {
		return [3]float64{0, 0, 1}
	}
	return [3]float64{v[0] / mag, v[1] / mag, v[2] / mag}
}
