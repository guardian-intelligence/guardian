package wire

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// The golden vectors below are the cross-language contract: the Rust
// session core and the TS host must emit these exact bytes for these exact
// inputs. They are written out by hand from the protocol layout, not
// captured from this encoder, so a change here that "fixes" a test is a
// protocol change and needs the other two implementations moved with it.

// Sim event kinds referenced by the vectors; the authority owns the full
// list.
const (
	evJoin     = 1
	evCheckIn  = 3
	evMoveTo   = 4
	evBoostSet = 8
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatalf("bad golden vector: %v", err)
	}
	return b
}

func TestVarintRFC9000(t *testing.T) {
	// Appendix A.1's sample encodings, plus the non-minimal two-byte form
	// of 37 that a decoder must still accept.
	cases := []struct {
		enc      string
		v        uint64
		minimal  bool
		encLenIs int
	}{
		{"c2197c5eff14e88c", 151288809941952652, true, 8},
		{"9d7f3e7d", 494878333, true, 4},
		{"7bbd", 15293, true, 2},
		{"25", 37, true, 1},
		{"4025", 37, false, 2},
	}
	for _, tc := range cases {
		raw := mustHex(t, tc.enc)
		got, err := ReadVarint(bufio.NewReader(bytes.NewReader(raw)))
		if err != nil || got != tc.v {
			t.Fatalf("ReadVarint(%s) = %d, %v; want %d", tc.enc, got, err, tc.v)
		}
		if !tc.minimal {
			continue
		}
		if enc := AppendVarint(nil, tc.v); !bytes.Equal(enc, raw) {
			t.Fatalf("AppendVarint(%d) = %x; want %s", tc.v, enc, tc.enc)
		}
		if n := VarintLen(tc.v); n != tc.encLenIs {
			t.Fatalf("VarintLen(%d) = %d; want %d", tc.v, n, tc.encLenIs)
		}
	}
}

func TestVarintBoundaries(t *testing.T) {
	for _, v := range []uint64{0, 63, 64, 16383, 16384, 1073741823, 1073741824, 1 << 40} {
		enc := AppendVarint(nil, v)
		if len(enc) != VarintLen(v) {
			t.Fatalf("AppendVarint(%d) wrote %d bytes; VarintLen says %d", v, len(enc), VarintLen(v))
		}
		got, err := ReadVarint(bufio.NewReader(bytes.NewReader(enc)))
		if err != nil || got != v {
			t.Fatalf("varint round-trip %d: got %d, %v", v, got, err)
		}
	}
}

// Shared fixture values, chosen so every byte position differs from its
// neighbours and a transposed field shows up as a diff.
const (
	fxTick    = 1024
	fxSeq     = 9
	fxTerrain = 0x0123456789abcdef
	fxWH      = 0xfeedfacecafebeef
	fxCTMS    = 1700000000000 // 0x0000018bcfe56800
)

func TestFrameGoldens(t *testing.T) {
	cases := []struct {
		name string
		enc  []byte
		want string
	}{
		{
			// proto 4, a client that has nothing yet (seq -1, tick 0).
			name: "hello",
			enc:  EncodeHello(Hello{Proto: Proto, SinceSeq: -1, SinceTick: 0, Ticket: "tkt"}),
			want: "18 01" +
				"0400" +
				"ffffffffffffffff" +
				"0000000000000000" +
				"0300" + "746b74",
		},
		{
			// move_to: dog id then node index, the sim's payload.
			name: "intent",
			enc: EncodeIntent(Intent{
				ID: 0x0102030405060708, Kind: evMoveTo,
				Payload: mustHex(t, "8877665544332211 0700"),
			}),
			want: "17 02" +
				"0807060504030201" +
				"0400" +
				"0a00" + "88776655443322110700",
		},
		{
			name: "resync",
			enc:  EncodeResync(Resync{HaveSeq: 4242}),
			want: "09 03" + "9210000000000000",
		},
		{
			name: "welcome",
			enc: EncodeWelcome(Welcome{
				Epoch: 1, Seq: 7, Tick: fxTick, Hz: 24,
				Role: RolePlayer, Terrain: fxTerrain, Park: "park-mythra",
			}),
			want: "2e 10" +
				"01000000" +
				"0700000000000000" +
				"0004000000000000" +
				"18000000" +
				"01" +
				"efcdab8967452301" +
				"0b" + "7061726b2d6d7974687261",
		},
		{
			// check_in by the dog that asked for it: intent id echoes back
			// so the sender can retire its pending entry.
			name: "event",
			enc: EncodeEvent(Event{
				Seq: fxSeq, Tick: fxTick, Kind: evCheckIn, Intent: 5,
				Payload: mustHex(t, "8877665544332211"),
			}),
			want: "25 11" +
				"0900000000000000" +
				"0004000000000000" +
				"0300" +
				"0500000000000000" +
				"0800" + "8877665544332211",
		},
		{
			name: "reject",
			enc:  EncodeReject(Reject{Intent: 5, Reason: 101}),
			want: "0d 12" + "0500000000000000" + "65000000",
		},
		{
			name: "snapshot",
			enc: EncodeSnapshot(Snapshot{
				Seq: fxSeq, Tick: fxTick, Epoch: 1, WH: fxWH,
				Terrain: fxTerrain, Z: mustHex(t, "deadbeef"),
			}),
			want: "2d 13" +
				"0900000000000000" +
				"0004000000000000" +
				"01000000" +
				"efbefecacefaedfe" +
				"efcdab8967452301" +
				"04000000" + "deadbeef",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := mustHex(t, tc.want)
			if !bytes.Equal(tc.enc, want) {
				t.Fatalf("golden mismatch\n got %x\nwant %x", tc.enc, want)
			}
		})
	}
}

func TestDatagramGoldens(t *testing.T) {
	check := EncodeCheck(Check{Tick: fxTick, WH: fxWH, CTMS: fxCTMS})
	wantCheck := mustHex(t, "01"+
		"0004000000000000"+
		"efbefecacefaedfe"+
		"0068e5cf8b010000")
	if !bytes.Equal(check, wantCheck) {
		t.Fatalf("check golden mismatch\n got %x\nwant %x", check, wantCheck)
	}
	if len(check) != CheckLen {
		t.Fatalf("check is %d bytes; the layout says %d", len(check), CheckLen)
	}

	verdict := EncodeVerdict(Verdict{
		Tick: fxTick, Now: 1030, CTMS: fxCTMS,
		Flags: VerdictKnown | VerdictOK,
		CW:    ModuleWord("9abcdef0"), PW: ModuleWord("12345678"),
	})
	// cw/pw land on the wire as the hash bytes in display order.
	wantVerdict := mustHex(t, "02"+
		"0004000000000000"+
		"0604000000000000"+
		"0068e5cf8b010000"+
		"03"+
		"9abcdef0"+
		"12345678")
	if !bytes.Equal(verdict, wantVerdict) {
		t.Fatalf("verdict golden mismatch\n got %x\nwant %x", verdict, wantVerdict)
	}
	if len(verdict) != VerdictLen {
		t.Fatalf("verdict is %d bytes; the layout says %d", len(verdict), VerdictLen)
	}
}

func TestModuleWord(t *testing.T) {
	// The display hash is hex of sha256[:4]; the u32 is those bytes read
	// little-endian, so re-encoding reproduces the display order exactly.
	if got := ModuleWord("9abcdef0"); got != 0xf0debc9a {
		t.Fatalf("ModuleWord(9abcdef0) = %08x; want f0debc9a", got)
	}
	if got := HexWord(ModuleWord("9abcdef0")); got != "9abcdef0" {
		t.Fatalf("HexWord round-trip = %q; want 9abcdef0", got)
	}
	if got := ModuleWord(""); got != 0 {
		t.Fatalf("ModuleWord of an unloaded module = %08x; want 0", got)
	}
	if got := ModuleWord("zzzzzzzz"); got != 0 {
		t.Fatalf("ModuleWord of a non-hex string = %08x; want 0", got)
	}
}

// Each encoder declares its payload length before writing the fields, so
// the two can drift. Nothing downstream notices a length that is merely
// short — the next frame just starts inside this one — which makes this
// worth checking directly.
func TestLengthPrefixMatchesBody(t *testing.T) {
	frames := [][]byte{
		EncodeHello(Hello{Proto: Proto, Ticket: "ticket"}),
		EncodeIntent(Intent{Payload: []byte{1, 2, 3}}),
		EncodeResync(Resync{HaveSeq: 1}),
		EncodeWelcome(Welcome{Park: "park-mythra"}),
		EncodeEvent(Event{Payload: []byte{1}}),
		EncodeReject(Reject{}),
		EncodeSnapshot(Snapshot{Z: []byte{1, 2, 3, 4, 5}}),
		EncodeSnapshot(Snapshot{Z: bytes.Repeat([]byte{0}, 70000)}),
	}
	for _, f := range frames {
		body, err := ReadVarint(bufio.NewReader(bytes.NewReader(f)))
		if err != nil {
			t.Fatalf("varint: %v", err)
		}
		kind := f[VarintLen(body)]
		if want := uint64(len(f)) - uint64(VarintLen(body)); body != want {
			t.Fatalf("kind %d declares a %d-byte body but wrote %d", kind, body, want)
		}
		if got := FrameSize(len(f) - VarintLen(body) - 1); got != len(f) {
			t.Fatalf("kind %d: FrameSize says %d, frame is %d", kind, got, len(f))
		}
	}
}

func TestFrameRoundTrip(t *testing.T) {
	// One instance of every kind, written into a stream and read back
	// through the decoder the session uses.
	hello := Hello{Proto: Proto, SinceSeq: -1, Ticket: "a-ticket"}
	intent := Intent{ID: 7, Kind: evBoostSet, Payload: []byte{1, 2, 3, 4, 5, 6, 7, 8, 1}}
	resync := Resync{HaveSeq: 12345}
	welcome := Welcome{Epoch: 3, Seq: fxSeq, Tick: fxTick, Hz: 24, Role: RoleSpectator, Terrain: fxTerrain, Park: "park-mythra"}
	event := Event{Seq: fxSeq, Tick: fxTick, Kind: evJoin, Intent: 11, Payload: []byte{9, 9}}
	reject := Reject{Intent: 11, Reason: 100}
	snapshot := Snapshot{Seq: fxSeq, Tick: fxTick, Epoch: 3, WH: fxWH, Terrain: fxTerrain, Z: bytes.Repeat([]byte{0xa5}, 40000)}

	var stream bytes.Buffer
	for _, b := range [][]byte{
		EncodeHello(hello), EncodeIntent(intent), EncodeResync(resync),
		EncodeWelcome(welcome), EncodeEvent(event), EncodeReject(reject),
		EncodeSnapshot(snapshot),
	} {
		stream.Write(b)
	}

	fr := NewReader(&stream)
	type decoded struct {
		kind byte
		val  any
	}
	var got []decoded
	for {
		kind, payload, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("frame read: %v", err)
		}
		var v any
		switch kind {
		case KindHello:
			v, err = DecodeHello(payload)
		case KindIntent:
			v, err = DecodeIntent(payload)
		case KindResync:
			v, err = DecodeResync(payload)
		case KindWelcome:
			v, err = DecodeWelcome(payload)
		case KindEvent:
			v, err = DecodeEvent(payload)
		case KindReject:
			v, err = DecodeReject(payload)
		case KindSnapshot:
			v, err = DecodeSnapshot(payload)
		default:
			t.Fatalf("unknown frame kind %d", kind)
		}
		if err != nil {
			t.Fatalf("decode kind %d: %v", kind, err)
		}
		got = append(got, decoded{kind, v})
	}

	want := []decoded{
		{KindHello, hello}, {KindIntent, intent}, {KindResync, resync},
		{KindWelcome, welcome}, {KindEvent, event}, {KindReject, reject},
		{KindSnapshot, snapshot},
	}
	if len(got) != len(want) {
		t.Fatalf("read %d frames; wrote %d", len(got), len(want))
	}
	for i := range want {
		if got[i].kind != want[i].kind || !reflect.DeepEqual(got[i].val, want[i].val) {
			t.Fatalf("frame %d round-trip\n got %d %#v\nwant %d %#v",
				i, got[i].kind, got[i].val, want[i].kind, want[i].val)
		}
	}
}

func TestDatagramRoundTrip(t *testing.T) {
	chk := Check{Tick: fxTick, WH: fxWH, CTMS: fxCTMS}
	if got, err := DecodeCheck(EncodeCheck(chk)); err != nil || got != chk {
		t.Fatalf("check round-trip: %#v, %v", got, err)
	}
	for _, flags := range []uint8{0, VerdictKnown, VerdictKnown | VerdictOK} {
		v := Verdict{Tick: fxTick, Now: fxTick + 6, CTMS: fxCTMS, Flags: flags, CW: 1, PW: 2}
		if got, err := DecodeVerdict(EncodeVerdict(v)); err != nil || got != v {
			t.Fatalf("verdict round-trip: %#v, %v", got, err)
		}
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	full := EncodeEvent(Event{Seq: 1, Tick: 2, Kind: evJoin, Intent: 3, Payload: []byte{4}})
	payload := full[2:] // past the varint and kind byte

	if _, err := DecodeEvent(payload[:len(payload)-1]); err == nil {
		t.Fatal("truncated event payload decoded")
	}
	if _, err := DecodeEvent(append(append([]byte{}, payload...), 0xff)); err == nil {
		t.Fatal("event payload with trailing bytes decoded")
	}
	// A length field larger than the bytes that follow must not be trusted.
	bad := EncodeSnapshot(Snapshot{Z: []byte{1, 2, 3, 4}})[2:]
	bad[39] = 0xff // top byte of zlen, at offset 36 of the payload
	if _, err := DecodeSnapshot(bad); err == nil {
		t.Fatal("snapshot with an overlong zlen decoded")
	}
	if _, err := DecodeHello(nil); err == nil {
		t.Fatal("empty hello payload decoded")
	}
	if _, err := DecodeCheck(EncodeVerdict(Verdict{})); err == nil {
		t.Fatal("verdict decoded as a check")
	}
	if _, err := DecodeCheck(EncodeCheck(Check{})[:24]); err == nil {
		t.Fatal("truncated check decoded")
	}
}

func TestReaderRejectsOversize(t *testing.T) {
	var b bytes.Buffer
	b.Write(AppendVarint(nil, MaxFrameLen+1))
	if _, _, err := NewReader(&b).Next(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame: got %v; want ErrFrameTooLarge", err)
	}

	b.Reset()
	b.Write(AppendVarint(nil, 0))
	if _, _, err := NewReader(&b).Next(); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("empty frame body: got %v; want ErrShortFrame", err)
	}

	// A truncated body is a short read, not a decode: the peer went away
	// mid-frame and the session ends.
	b.Reset()
	b.Write(AppendVarint(nil, 16))
	b.Write([]byte{KindEvent, 1, 2, 3})
	if _, _, err := NewReader(&b).Next(); err == nil {
		t.Fatal("truncated frame body read as a whole frame")
	}
}

func TestRoleCode(t *testing.T) {
	if RoleCode("player") != RolePlayer || RoleCode("spectator") != RoleSpectator || RoleCode("") != RoleSpectator {
		t.Fatal("role encoding does not match the wire's 0 spectator / 1 player")
	}
}
