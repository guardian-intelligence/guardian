package gametest

import "fmt"

// Structural scans over the wasm binary itself. These gates hold for the
// artifact regardless of what source produced it: a module with no imports
// cannot reach a clock, entropy, or the network, and a module declaring no
// float types cannot compute differently across runtimes.

func scanFloatDecls(b []byte) error {
	r, err := newWasmReader(b)
	if err != nil {
		return err
	}
	for !r.done() {
		id := r.byte()
		body := r.bytes(r.u32())
		s := wasmReader{b: body}
		switch id {
		case 1: // type section: vec(functype)
			for n := s.u32(); n > 0; n-- {
				if tag := s.byte(); tag != 0x60 {
					return fmt.Errorf("unexpected functype tag %#x", tag)
				}
				for p := s.u32(); p > 0; p-- {
					if err := checkValType(s.byte(), "param"); err != nil {
						return err
					}
				}
				for q := s.u32(); q > 0; q-- {
					if err := checkValType(s.byte(), "result"); err != nil {
						return err
					}
				}
			}
		case 6: // global section: vec(globaltype expr)
			for n := s.u32(); n > 0; n-- {
				if err := checkValType(s.byte(), "global"); err != nil {
					return err
				}
				s.byte() // mutability
				s.skipConstExpr()
			}
		case 10: // code section: vec(size locals... body)
			for n := s.u32(); n > 0; n-- {
				fn := wasmReader{b: s.bytes(s.u32())}
				for d := fn.u32(); d > 0; d-- {
					fn.u32() // repeat count
					if err := checkValType(fn.byte(), "local"); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// scanImports returns every import as "module.name". A deterministic game
// module must import nothing.
func scanImports(b []byte) ([]string, error) {
	r, err := newWasmReader(b)
	if err != nil {
		return nil, err
	}
	var imports []string
	for !r.done() {
		id := r.byte()
		body := r.bytes(r.u32())
		if id != 2 { // import section
			continue
		}
		s := wasmReader{b: body}
		for n := s.u32(); n > 0; n-- {
			mod := string(s.bytes(s.u32()))
			name := string(s.bytes(s.u32()))
			imports = append(imports, mod+"."+name)
			switch s.byte() { // importdesc
			case 0x00: // func: typeidx
				s.u32()
			case 0x01: // table: reftype + limits
				s.byte()
				s.skipLimits()
			case 0x02: // mem: limits
				s.skipLimits()
			case 0x03: // global: valtype + mut
				s.byte()
				s.byte()
			}
		}
	}
	return imports, nil
}

// scanExports returns every export name (any kind).
func scanExports(b []byte) ([]string, error) {
	r, err := newWasmReader(b)
	if err != nil {
		return nil, err
	}
	var exports []string
	for !r.done() {
		id := r.byte()
		body := r.bytes(r.u32())
		if id != 7 { // export section
			continue
		}
		s := wasmReader{b: body}
		for n := s.u32(); n > 0; n-- {
			exports = append(exports, string(s.bytes(s.u32())))
			s.byte() // exportdesc kind
			s.u32()  // index
		}
	}
	return exports, nil
}

func checkValType(v byte, where string) error {
	if v == 0x7D || v == 0x7C { // f32, f64
		return fmt.Errorf("float value type %#x declared as %s", v, where)
	}
	return nil
}

type wasmReader struct {
	b   []byte
	off int
}

func newWasmReader(b []byte) (*wasmReader, error) {
	if len(b) < 8 || string(b[:4]) != "\x00asm" {
		return nil, fmt.Errorf("not a wasm module (%d bytes)", len(b))
	}
	return &wasmReader{b: b, off: 8}, nil
}

func (r *wasmReader) done() bool { return r.off >= len(r.b) }

func (r *wasmReader) byte() byte {
	if r.off >= len(r.b) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *wasmReader) u32() uint32 {
	var v uint32
	for shift := 0; shift < 35; shift += 7 {
		c := r.byte()
		v |= uint32(c&0x7F) << shift
		if c&0x80 == 0 {
			break
		}
	}
	return v
}

func (r *wasmReader) bytes(n uint32) []byte {
	end := min(r.off+int(n), len(r.b))
	v := r.b[r.off:end]
	r.off = end
	return v
}

func (r *wasmReader) skipLimits() {
	flags := r.byte()
	r.u32()
	if flags&1 != 0 {
		r.u32()
	}
}

// skipConstExpr advances past a global initializer (ends with 0x0B). Only
// the encodings a no-import module can produce need handling.
func (r *wasmReader) skipConstExpr() {
	for {
		switch r.byte() {
		case 0x0B:
			return
		case 0x41: // i32.const
			r.u32()
		case 0x42: // i64.const
			for shift := 0; shift < 70; shift += 7 {
				if r.byte()&0x80 == 0 {
					break
				}
			}
		case 0x23: // global.get
			r.u32()
		default:
			return
		}
	}
}
