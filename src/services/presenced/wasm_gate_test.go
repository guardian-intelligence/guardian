package main

import (
	"fmt"
	"testing"
)

// Fixed-point invariant, layer 2: the embedded behavior modules must
// declare no float value types anywhere wasm declares types - function
// signatures, globals, or locals. Together with the source token gate in
// //src/services/presenced/sim this keeps the sim integer-only at the
// artifact level, so identical bytes behave identically under any runtime,
// interpreter, or future native port.
func TestBehaviorModulesDeclareNoFloats(t *testing.T) {
	for name, module := range map[string][]byte{
		"server.wasm": defaultBehavior,
		"client.wasm": defaultClientModule,
	} {
		if err := scanForFloatDecls(module); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func scanForFloatDecls(b []byte) error {
	if len(b) < 8 || string(b[:4]) != "\x00asm" {
		return fmt.Errorf("not a wasm module")
	}
	r := &wasmReader{b: b, off: 8}
	for !r.done() {
		id := r.byte()
		size := r.u32()
		body := r.bytes(size)
		s := &wasmReader{b: body}
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
				bodySize := s.u32()
				fn := &wasmReader{b: s.bytes(bodySize)}
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

// skipConstExpr advances past a global initializer (ends with 0x0B). Only
// the encodings a no-import module can produce need handling.
func (r *wasmReader) skipConstExpr() {
	for {
		op := r.byte()
		switch op {
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
