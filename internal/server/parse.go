package server

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/phonevoxel/internal/types"
)

// parseClientMessage decodes the binary wire format from a phone client.
//
// Binary layout (see types/messages.go):
//   Bytes  0-7:   float64  timestamp
//   Bytes  8-15:  float64  latitude
//   Bytes 16-23:  float64  longitude
//   Bytes 24-31:  float64  altitude
//   Bytes 32-39:  float64  gps_accuracy
//   Bytes 40-47:  float64  alpha (compass)
//   Bytes 48-55:  float64  beta (pitch)
//   Bytes 56-63:  float64  gamma (roll)
//   Bytes 64-71:  float64  hfov
//   Bytes 72-75:  uint32   frame_width
//   Bytes 76-79:  uint32   frame_height
//   Bytes 80-83:  uint32   jpeg_length
//   Bytes 84+:    []byte   jpeg_data
func parseClientMessage(data []byte) (*types.ClientFrame, error) {
	if len(data) < types.HeaderSize {
		return nil, fmt.Errorf("message too short: %d bytes", len(data))
	}

	f := &types.ClientFrame{}
	f.ClientTimestamp = math.Float64frombits(binary.LittleEndian.Uint64(data[0:8]))
	f.Lat             = math.Float64frombits(binary.LittleEndian.Uint64(data[8:16]))
	f.Lon             = math.Float64frombits(binary.LittleEndian.Uint64(data[16:24]))
	f.Alt             = math.Float64frombits(binary.LittleEndian.Uint64(data[24:32]))
	f.GPSAccuracy     = math.Float64frombits(binary.LittleEndian.Uint64(data[32:40]))
	f.Alpha           = math.Float64frombits(binary.LittleEndian.Uint64(data[40:48]))
	f.Beta            = math.Float64frombits(binary.LittleEndian.Uint64(data[48:56]))
	f.Gamma           = math.Float64frombits(binary.LittleEndian.Uint64(data[56:64]))
	f.HFOV            = math.Float64frombits(binary.LittleEndian.Uint64(data[64:72]))
	f.FrameWidth      = binary.LittleEndian.Uint32(data[72:76])
	f.FrameHeight     = binary.LittleEndian.Uint32(data[76:80])
	jpegLen           := binary.LittleEndian.Uint32(data[80:84])

	if len(data) < types.HeaderSize+int(jpegLen) {
		return nil, fmt.Errorf("truncated JPEG: expected %d bytes, got %d",
			jpegLen, len(data)-types.HeaderSize)
	}

	f.JPEG = make([]byte, jpegLen)
	copy(f.JPEG, data[types.HeaderSize:types.HeaderSize+int(jpegLen)])
	return f, nil
}
