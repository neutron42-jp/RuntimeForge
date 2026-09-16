package gguf

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

type builder struct {
	bytes.Buffer
	kv int
}

func (b *builder) u32(v uint32) { _ = binary.Write(&b.Buffer, binary.LittleEndian, v) }
func (b *builder) u64(v uint64) { _ = binary.Write(&b.Buffer, binary.LittleEndian, v) }
func (b *builder) str(s string) {
	b.u64(uint64(len(s)))
	b.WriteString(s)
}

func (b *builder) header(tensors uint64) {
	b.u32(Magic)
	b.u32(3)
	b.u64(tensors)
	// kv count placeholder, patched by finish.
	b.u64(0)
}

func (b *builder) finish() []byte {
	out := b.Bytes()
	binary.LittleEndian.PutUint64(out[16:24], uint64(b.kv))
	return out
}

func (b *builder) kvString(key, val string) {
	b.kv++
	b.str(key)
	b.u32(TypeString)
	b.str(val)
}

func (b *builder) kvU32(key string, v uint32) {
	b.kv++
	b.str(key)
	b.u32(TypeUint32)
	b.u32(v)
}

func (b *builder) kvI32(key string, v int32) {
	b.kv++
	b.str(key)
	b.u32(TypeInt32)
	b.u32(uint32(v))
}

func (b *builder) kvF32(key string, v float32) {
	b.kv++
	b.str(key)
	b.u32(TypeFloat32)
	b.u32(math.Float32bits(v))
}

func (b *builder) kvBool(key string, v bool) {
	b.kv++
	b.str(key)
	b.u32(TypeBool)
	if v {
		b.WriteByte(1)
	} else {
		b.WriteByte(0)
	}
}

func (b *builder) kvArrayU32(key string, vals []uint32) {
	b.kv++
	b.str(key)
	b.u32(TypeArray)
	b.u32(TypeUint32)
	b.u64(uint64(len(vals)))
	for _, v := range vals {
		b.u32(v)
	}
}

func (b *builder) kvArrayString(key string, vals []string) {
	b.kv++
	b.str(key)
	b.u32(TypeArray)
	b.u32(TypeString)
	b.u64(uint64(len(vals)))
	for _, v := range vals {
		b.str(v)
	}
}

func TestParseHeaderAndScalars(t *testing.T) {
	b := &builder{}
	b.header(291)
	b.kvString(KeyArchitecture, "qwen3")
	b.kvString(KeyName, "Qwen3 8B")
	b.kvString(KeySizeLabel, "8B")
	b.kvU32(KeyFileType, 15)
	b.kvU32(KeyQuantVersion, 2)
	b.kvI32("some.negative", -42)
	b.kvF32("some.float", 3.5)
	b.kvBool("some.bool", true)

	f, err := Read(bytes.NewReader(b.finish()))
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 3 {
		t.Errorf("version = %d", f.Version)
	}
	if f.TensorCount != 291 {
		t.Errorf("tensor count = %d", f.TensorCount)
	}
	if f.KVCount != 8 {
		t.Errorf("kv count = %d", f.KVCount)
	}
	if f.Architecture() != "qwen3" {
		t.Errorf("arch = %q", f.Architecture())
	}
	if f.Name() != "Qwen3 8B" {
		t.Errorf("name = %q", f.Name())
	}
	if f.SizeLabel() != "8B" {
		t.Errorf("size label = %q", f.SizeLabel())
	}
	if f.Quantization() != "Q4_K_M" {
		t.Errorf("quant = %q", f.Quantization())
	}
	if got := f.Metadata["some.negative"].Scalar; got != int32(-42) {
		t.Errorf("int32 = %v (%T)", got, got)
	}
	if got := f.Metadata["some.float"].Scalar; got != float32(3.5) {
		t.Errorf("float32 = %v (%T)", got, got)
	}
	if got := f.Metadata["some.bool"].Scalar; got != true {
		t.Errorf("bool = %v", got)
	}
}

func TestArraySkipping(t *testing.T) {
	b := &builder{}
	b.header(1)
	b.kvString(KeyArchitecture, "llama")
	// large token array that must be skipped, followed by a real key
	// proving the reader landed in the right place.
	tokens := make([]string, 1000)
	for i := range tokens {
		tokens[i] = "tok" + string(rune('a'+i%26))
	}
	b.kvArrayString("tokenizer.ggml.tokens", tokens)
	b.kvArrayU32("tokenizer.ggml.token_type", []uint32{1, 2, 3, 4, 5})
	b.kvString(KeyName, "after-arrays")

	f, err := Read(bytes.NewReader(b.finish()))
	if err != nil {
		t.Fatal(err)
	}
	if v := f.Metadata["tokenizer.ggml.tokens"]; v.Type != TypeArray || v.ArrayLen != 1000 || v.ArrayType != TypeString {
		t.Errorf("tokens value = %+v", v)
	}
	if v := f.Metadata["tokenizer.ggml.token_type"]; v.ArrayLen != 5 || v.ArrayType != TypeUint32 {
		t.Errorf("token_type value = %+v", v)
	}
	if f.Name() != "after-arrays" {
		t.Errorf("name = %q (reader misaligned after arrays)", f.Name())
	}
	if v := f.Metadata["tokenizer.ggml.tokens"].Scalar; v != nil {
		t.Errorf("array scalar should be nil, got %v", v)
	}
}

func TestReadRejectsBadMagic(t *testing.T) {
	data := make([]byte, 24)
	if _, err := Read(bytes.NewReader(data)); err == nil {
		t.Fatal("expected error for bad magic")
	}
}

func TestReadRejectsTruncated(t *testing.T) {
	b := &builder{}
	b.header(1)
	b.kvString(KeyArchitecture, "llama")
	data := b.finish()
	if _, err := Read(bytes.NewReader(data[:len(data)-2])); err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestQuantizationFallback(t *testing.T) {
	b := &builder{}
	b.header(0)
	b.kvU32(KeyFileType, 999)
	f, err := Read(bytes.NewReader(b.finish()))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Quantization(); got != "ftype_999" {
		t.Errorf("quant = %q", got)
	}
}

func TestParseShard(t *testing.T) {
	cases := []struct {
		path  string
		ok    bool
		index int
		total int
		base  string
	}{
		{"/models/Qwen3-8B-Q4_K_M-00001-of-00003.gguf", true, 1, 3, "Qwen3-8B-Q4_K_M"},
		{"model-00002-of-00005.gguf", true, 2, 5, "model"},
		{"/models/single.gguf", false, 0, 0, ""},
		{"/models/not-gguf.bin", false, 0, 0, ""},
	}
	for _, c := range cases {
		got, ok := ParseShard(c.path)
		if ok != c.ok {
			t.Errorf("ParseShard(%q) ok = %v, want %v", c.path, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Index != c.index || got.Total != c.total || got.Base != c.base {
			t.Errorf("ParseShard(%q) = %+v", c.path, got)
		}
	}
}
