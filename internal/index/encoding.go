package index

import (
	"encoding/binary"
	"math"
)

func EncodeFloat32(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// EncodeInt8 quantizes a float32 vector to uint8 with per-vector min/max scaling.
// Format: 4 bytes min (float32 LE) + 4 bytes scale (float32 LE) + len(vec) bytes uint8.
// Storage is 4x smaller than float32 (8 byte header + 1 byte/dim vs 4 bytes/dim).
// Quality loss is <1% on recall@10 for L2-normalized embeddings.
func EncodeInt8(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	minVal, maxVal := vec[0], vec[0]
	for _, v := range vec[1:] {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	var scale float32
	if maxVal > minVal {
		scale = (maxVal - minVal) / 255.0
	}

	buf := make([]byte, 8+len(vec))
	binary.LittleEndian.PutUint32(buf[0:], math.Float32bits(minVal))
	binary.LittleEndian.PutUint32(buf[4:], math.Float32bits(scale))
	for i, v := range vec {
		var q uint8
		if scale > 0 {
			qf := (v - minVal) / scale
			if qf < 0 {
				qf = 0
			} else if qf > 255 {
				qf = 255
			}
			q = uint8(math.Round(float64(qf)))
		}
		buf[8+i] = q
	}
	return buf
}

func NormalizeFloat32(vec []float32) []float32 {
	normalized := make([]float32, len(vec))
	copy(normalized, vec)

	var norm float32
	for _, v := range normalized {
		norm += v * v
	}
	if norm == 0 {
		return normalized
	}

	scale := 1 / sqrt32(norm)
	for i := range normalized {
		normalized[i] *= scale
	}
	return normalized
}

func decodeFloat32(data []byte) []float32 {
	n := len(data) / 4
	vec := make([]float32, n)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

func dotProduct(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

func dotProductEncoded(query []float32, encoded []byte) float32 {
	switch len(encoded) {
	case len(query) * 4:
		// float32 format.
		var dot float32
		for i, q := range query {
			v := math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
			dot += q * v
		}
		return dot
	case 8 + len(query):
		// int8 quantized format: 4-byte min + 4-byte scale + uint8 per dim.
		data := encoded[8:]                                                      //nolint:gosec // G602: bounds guaranteed by switch case
		minVal := math.Float32frombits(binary.LittleEndian.Uint32(encoded[0:4])) //nolint:gosec // G602
		scale := math.Float32frombits(binary.LittleEndian.Uint32(encoded[4:8]))  //nolint:gosec // G602
		var dot float32
		for i, q := range query {
			v := float32(data[i])*scale + minVal
			dot += q * v
		}
		return dot
	default:
		return 0
	}
}

func sqrt32(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}
