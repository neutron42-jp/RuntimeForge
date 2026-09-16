// Package gguf reads the metadata header of GGUF model files. Only the
// key/value header is parsed; tensor data is skipped. Array values are
// measured but not retained so large tokenizer arrays do not consume
// memory (SPEC §12).
package gguf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// Magic is the GGUF file signature ("GGUF" little-endian).
const Magic = 0x46554747

// Value type identifiers defined by the GGUF specification.
const (
	TypeUint8   = 0
	TypeInt8    = 1
	TypeUint16  = 2
	TypeInt16   = 3
	TypeUint32  = 4
	TypeInt32   = 5
	TypeFloat32 = 6
	TypeBool    = 7
	TypeString  = 8
	TypeArray   = 9
	TypeUint64  = 10
	TypeInt64   = 11
	TypeFloat64 = 12
)

// known metadata keys.
const (
	KeyArchitecture = "general.architecture"
	KeyName         = "general.name"
	KeySizeLabel    = "general.size_label"
	KeyFileType     = "general.file_type"
	KeyQuantVersion = "general.quantization_version"
)

// Value is a decoded metadata entry. For arrays only the element type
// and length are retained.
type Value struct {
	Type      uint32
	ArrayType uint32
	Scalar    any
	ArrayLen  uint64
}

// File is the parsed header of a GGUF file.
type File struct {
	Path        string
	Version     uint32
	TensorCount uint64
	KVCount     uint64
	Metadata    map[string]Value
	SizeBytes   int64
}

// String returns the string value of a metadata key, or "".
func (f *File) String(key string) string {
	v, ok := f.Metadata[key]
	if !ok {
		return ""
	}
	s, _ := v.Scalar.(string)
	return s
}

// Uint returns the unsigned integer value of a metadata key, or 0.
func (f *File) Uint(key string) uint64 {
	v, ok := f.Metadata[key]
	if !ok {
		return 0
	}
	switch n := v.Scalar.(type) {
	case uint8:
		return uint64(n)
	case uint16:
		return uint64(n)
	case uint32:
		return uint64(n)
	case uint64:
		return n
	case int8:
		return uint64(n)
	case int16:
		return uint64(n)
	case int32:
		return uint64(n)
	case int64:
		return uint64(n)
	default:
		return 0
	}
}

// Architecture returns general.architecture (e.g. "llama", "qwen3").
func (f *File) Architecture() string { return f.String(KeyArchitecture) }

// Name returns general.name.
func (f *File) Name() string { return f.String(KeyName) }

// SizeLabel returns general.size_label (e.g. "8B").
func (f *File) SizeLabel() string { return f.String(KeySizeLabel) }

// Quantization returns a human readable quantization name derived from
// general.file_type, falling back to the raw number.
func (f *File) Quantization() string {
	if v, ok := f.Metadata[KeyFileType]; ok {
		if name, ok := fileTypeNames[toInt(v.Scalar)]; ok {
			return name
		}
		return fmt.Sprintf("ftype_%d", toInt(v.Scalar))
	}
	return ""
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case uint8:
		return int64(n)
	case uint16:
		return int64(n)
	case uint32:
		return int64(n)
	case uint64:
		return int64(n)
	case int8:
		return int64(n)
	case int16:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}

// Open parses the header of the GGUF file at path.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	gf, err := Read(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	gf.Path = path
	gf.SizeBytes = info.Size()
	return gf, nil
}

// Read parses a GGUF header from r.
func Read(r io.Reader) (*File, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	// Buffered reader is enough; we only need forward-only skipping and
	// fixed-size skips are cheaper than seeking anyway for headers.
	return readHeader(br)
}

func readHeader(r *bufio.Reader) (*File, error) {
	magic, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if magic != Magic {
		return nil, errors.New("not a GGUF file (bad magic)")
	}
	version, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if version < 2 || version > 3 {
		return nil, fmt.Errorf("unsupported GGUF version %d", version)
	}
	tensorCount, err := readU64(r)
	if err != nil {
		return nil, fmt.Errorf("read tensor count: %w", err)
	}
	kvCount, err := readU64(r)
	if err != nil {
		return nil, fmt.Errorf("read kv count: %w", err)
	}

	f := &File{
		Version:     version,
		TensorCount: tensorCount,
		KVCount:     kvCount,
		Metadata:    make(map[string]Value, kvCount),
	}

	for i := uint64(0); i < kvCount; i++ {
		key, err := readString(r)
		if err != nil {
			return nil, fmt.Errorf("kv[%d] key: %w", i, err)
		}
		val, err := readValue(r)
		if err != nil {
			return nil, fmt.Errorf("kv[%d] %q: %w", i, key, err)
		}
		f.Metadata[key] = val
	}
	return f, nil
}

func readValue(r *bufio.Reader) (Value, error) {
	t, err := readU32(r)
	if err != nil {
		return Value{}, err
	}
	if t == TypeArray {
		elemType, err := readU32(r)
		if err != nil {
			return Value{}, err
		}
		n, err := readU64(r)
		if err != nil {
			return Value{}, err
		}
		if err := skipArray(r, elemType, n); err != nil {
			return Value{}, err
		}
		return Value{Type: TypeArray, ArrayType: elemType, ArrayLen: n}, nil
	}
	s, err := readScalar(r, t)
	if err != nil {
		return Value{}, err
	}
	return Value{Type: t, Scalar: s}, nil
}

func readScalar(r *bufio.Reader, t uint32) (any, error) {
	switch t {
	case TypeUint8:
		v, err := r.ReadByte()
		return v, err
	case TypeInt8:
		v, err := r.ReadByte()
		return int8(v), err
	case TypeUint16:
		v, err := readU16(r)
		return v, err
	case TypeInt16:
		v, err := readU16(r)
		return int16(v), err
	case TypeUint32:
		return readU32(r)
	case TypeInt32:
		v, err := readU32(r)
		return int32(v), err
	case TypeFloat32:
		v, err := readU32(r)
		return math.Float32frombits(v), err
	case TypeBool:
		v, err := r.ReadByte()
		return v != 0, err
	case TypeString:
		return readString(r)
	case TypeUint64:
		return readU64(r)
	case TypeInt64:
		v, err := readU64(r)
		return int64(v), err
	case TypeFloat64:
		v, err := readU64(r)
		return math.Float64frombits(v), err
	default:
		return nil, fmt.Errorf("unknown value type %d", t)
	}
}

// maxStringLen guards against corrupt length fields.
const maxStringLen = 1 << 30

func readString(r *bufio.Reader) (string, error) {
	n, err := readU64(r)
	if err != nil {
		return "", err
	}
	if n > maxStringLen {
		return "", fmt.Errorf("string length %d exceeds limit", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func skipArray(r *bufio.Reader, elemType uint32, n uint64) error {
	if n == 0 {
		return nil
	}
	if size, ok := fixedSizes[elemType]; ok {
		const chunk = 1 << 20
		remaining := int64(size) * int64(n)
		for remaining > 0 {
			step := int64(chunk)
			if step > remaining {
				step = remaining
			}
			if _, err := io.CopyN(io.Discard, r, step); err != nil {
				return err
			}
			remaining -= step
		}
		return nil
	}
	switch elemType {
	case TypeString:
		for i := uint64(0); i < n; i++ {
			l, err := readU64(r)
			if err != nil {
				return err
			}
			if l > maxStringLen {
				return fmt.Errorf("array string length %d exceeds limit", l)
			}
			if _, err := io.CopyN(io.Discard, r, int64(l)); err != nil {
				return err
			}
		}
		return nil
	case TypeArray:
		for i := uint64(0); i < n; i++ {
			sub, err := readU32(r)
			if err != nil {
				return err
			}
			subN, err := readU64(r)
			if err != nil {
				return err
			}
			if err := skipArray(r, sub, subN); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("cannot skip unknown array element type %d", elemType)
	}
}

var fixedSizes = map[uint32]int{
	TypeUint8:   1,
	TypeInt8:    1,
	TypeUint16:  2,
	TypeInt16:   2,
	TypeUint32:  4,
	TypeInt32:   4,
	TypeFloat32: 4,
	TypeBool:    1,
	TypeUint64:  8,
	TypeInt64:   8,
	TypeFloat64: 8,
}

func readU16(r io.Reader) (uint16, error) {
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b[:]), nil
}

func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func readU64(r io.Reader) (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

// Shard describes split-GGUF membership derived from the file name.
type Shard struct {
	Index int
	Total int
	Base  string
}

// ParseShard detects the "model-00001-of-00003.gguf" naming scheme.
func ParseShard(path string) (Shard, bool) {
	name := path
	if i := strings.LastIndexAny(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if !strings.HasSuffix(name, ".gguf") {
		return Shard{}, false
	}
	stem := strings.TrimSuffix(name, ".gguf")
	// expect ...-NNNNN-of-NNNNN
	ofIdx := strings.LastIndex(stem, "-of-")
	if ofIdx < 0 {
		return Shard{}, false
	}
	totalPart := stem[ofIdx+4:]
	dashIdx := strings.LastIndex(stem[:ofIdx], "-")
	if dashIdx < 0 {
		return Shard{}, false
	}
	indexPart := stem[dashIdx+1 : ofIdx]
	index, ok1 := atoiStrict(indexPart)
	total, ok2 := atoiStrict(totalPart)
	if !ok1 || !ok2 || total <= 0 || index <= 0 {
		return Shard{}, false
	}
	return Shard{Index: index, Total: total, Base: stem[:dashIdx]}, true
}

func atoiStrict(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
