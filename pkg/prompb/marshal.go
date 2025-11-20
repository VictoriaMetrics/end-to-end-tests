package prompb

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
)

// MarshalProtobuf marshals wr to dst and returns the result.
func (wr *WriteRequest) MarshalProtobuf(dst []byte) []byte {
	size := wr.size()
	dstLen := len(dst)
	dst = setLength(dst, dstLen+size)
	n, err := wr.marshalToSizedBuffer(dst[dstLen:])
	if err != nil {
		panic(fmt.Errorf("BUG: unexpected error when marshaling WriteRequest: %w", err))
	}
	return dst[:dstLen+n]
}

func setLength(a []byte, newLen int) []byte {
	if n := newLen - cap(a); n > 0 {
		a = append(a[:cap(a)], make([]byte, n)...)
	}
	return a[:newLen]
}

func (m *Sample) marshalToSizedBuffer(dst []byte) (int, error) {
	i := len(dst)
	if m.Timestamp != 0 {
		i = encodeVarint(dst, i, uint64(m.Timestamp))
		i--
		dst[i] = (2 << 3)
	}
	if m.Value != 0 {
		i -= 8
		binary.LittleEndian.PutUint64(dst[i:], uint64(math.Float64bits(float64(m.Value))))
		i--
		dst[i] = (1 << 3) | 1
	}
	return len(dst) - i, nil
}

func (m *TimeSeries) marshalToSizedBuffer(dst []byte) (int, error) {
	i := len(dst)
	for j := len(m.Samples) - 1; j >= 0; j-- {
		size, err := m.Samples[j].marshalToSizedBuffer(dst[:i])
		if err != nil {
			return 0, err
		}
		i -= size
		i = encodeVarint(dst, i, uint64(size))
		i--
		dst[i] = (2 << 3) | 2
	}
	for j := len(m.Labels) - 1; j >= 0; j-- {
		size, err := m.Labels[j].marshalToSizedBuffer(dst[:i])
		if err != nil {
			return 0, err
		}
		i -= size
		i = encodeVarint(dst, i, uint64(size))
		i--
		dst[i] = (1 << 3) | 2
	}
	return len(dst) - i, nil
}

func (m *Label) marshalToSizedBuffer(dst []byte) (int, error) {
	i := len(dst)
	if len(m.Value) > 0 {
		i -= len(m.Value)
		copy(dst[i:], m.Value)
		i = encodeVarint(dst, i, uint64(len(m.Value)))
		i--
		dst[i] = (2 << 3) | 2
	}
	if len(m.Name) > 0 {
		i -= len(m.Name)
		copy(dst[i:], m.Name)
		i = encodeVarint(dst, i, uint64(len(m.Name)))
		i--
		dst[i] = (1 << 3) | 2
	}
	return len(dst) - i, nil
}

func (m *Sample) size() (n int) {
	if m == nil {
		return 0
	}
	if m.Value != 0 {
		n += 9
	}
	if m.Timestamp != 0 {
		n += 1 + sov(uint64(m.Timestamp))
	}
	return n
}

func (m *TimeSeries) size() (n int) {
	if m == nil {
		return 0
	}
	for _, e := range m.Labels {
		l := e.size()
		n += 1 + l + sov(uint64(l))
	}
	for _, e := range m.Samples {
		l := e.size()
		n += 1 + l + sov(uint64(l))
	}
	return n
}

func (m *Label) size() (n int) {
	if m == nil {
		return 0
	}
	if l := len(m.Name); l > 0 {
		n += 1 + l + sov(uint64(l))
	}
	if l := len(m.Value); l > 0 {
		n += 1 + l + sov(uint64(l))
	}
	return n
}

func (m *WriteRequest) marshalToSizedBuffer(dst []byte) (int, error) {
	i := len(dst)
	for j := len(m.Timeseries) - 1; j >= 0; j-- {
		size, err := m.Timeseries[j].marshalToSizedBuffer(dst[:i])
		if err != nil {
			return 0, err
		}
		i -= size
		i = encodeVarint(dst, i, uint64(size))
		i--
		dst[i] = (1 << 3) | 2
	}
	return len(dst) - i, nil
}

func (m *WriteRequest) size() (n int) {
	if m == nil {
		return 0
	}
	for _, e := range m.Timeseries {
		l := e.size()
		n += 1 + l + sov(uint64(l))
	}
	return n
}

func encodeVarint(dst []byte, offset int, v uint64) int {
	offset -= sov(v)
	base := offset
	for v >= 1<<7 {
		dst[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dst[offset] = uint8(v)
	return base
}

func sov(x uint64) (n int) {
	return (bits.Len64(x|1) + 6) / 7
}
