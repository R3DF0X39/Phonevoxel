package geo

import "math"

// Mat3 is a 3x3 rotation matrix stored in row-major order.
type Mat3 [3][3]float64

// OrientationToRotationMatrix converts W3C DeviceOrientationEvent Euler angles
// to a rotation matrix that transforms ENU world vectors into device (camera) space.
//
// Convention (W3C spec, ZXY Euler order):
//   alpha: compass heading rotation about world-Z (0° = north, clockwise)
//   beta:  pitch rotation about device-X (-180° to +180°)
//   gamma: roll rotation about device-Y (-90° to +90°)
//
// The resulting matrix R satisfies: v_device = R * v_world
func OrientationToRotationMatrix(alpha, beta, gamma float64) Mat3 {
	a := alpha * math.Pi / 180.0
	b := beta * math.Pi / 180.0
	g := gamma * math.Pi / 180.0

	cosA, sinA := math.Cos(a), math.Sin(a)
	cosB, sinB := math.Cos(b), math.Sin(b)
	cosG, sinG := math.Cos(g), math.Sin(g)

	// ZXY Euler composition per W3C Device Orientation spec.
	// R = Rz(alpha) * Rx(beta) * Ry(gamma)
	return Mat3{
		{cosA*cosG - sinA*sinB*sinG, -cosB*sinA, cosA*sinG + cosG*sinA*sinB},
		{cosG*sinA + cosA*sinB*sinG, cosA*cosB, sinA*sinG - cosA*cosG*sinB},
		{-cosB*sinG, sinB, cosB*cosG},
	}
}

// MulVec3 multiplies a Mat3 by a 3-vector (column vector on the right).
func (m Mat3) MulVec3(v [3]float64) [3]float64 {
	return [3]float64{
		m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2],
		m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2],
	}
}

// Transpose returns the transpose of the matrix (also its inverse for rotation matrices).
func (m Mat3) Transpose() Mat3 {
	return Mat3{
		{m[0][0], m[1][0], m[2][0]},
		{m[0][1], m[1][1], m[2][1]},
		{m[0][2], m[1][2], m[2][2]},
	}
}
