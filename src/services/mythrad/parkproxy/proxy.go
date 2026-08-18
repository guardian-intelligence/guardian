package parkproxy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	proxyMagic      = "CHUNKY01"
	proxyMaxPayload = 1 << 20
	KindStream     = 1
	KindDatagram   = 2
	KindClose      = 3
)

type Open struct {
	Sub       string
	Park      string
	Role      string
	Remote    string
	SinceSeq  int64
	SinceTick uint64
}

type Conn struct {
	net.Conn
	writeMu sync.Mutex
}

func ReadKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) < 32 {
		return nil, errors.New("internal key must contain at least 32 bytes")
	}
	return b, nil
}

func Dial(ctx context.Context, addr string, key []byte, open Open) (*Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	p := &Conn{Conn: c}
	if err := p.writeOpen(key, open); err != nil {
		c.Close()
		return nil, err
	}
	return p, nil
}

func Accept(c net.Conn, key []byte, now time.Time) (*Conn, Open, error) {
	p := &Conn{Conn: c}
	open, err := p.readOpen(key, now)
	if err != nil {
		c.Close()
		return nil, Open{}, err
	}
	return p, open, nil
}

func appendProxyString(dst []byte, s string) ([]byte, error) {
	if len(s) > 4096 {
		return nil, errors.New("proxy string too long")
	}
	dst = binary.LittleEndian.AppendUint16(dst, uint16(len(s)))
	return append(dst, s...), nil
}

func takeProxyString(b []byte, at *int) (string, error) {
	if *at+2 > len(b) {
		return "", io.ErrUnexpectedEOF
	}
	n := int(binary.LittleEndian.Uint16(b[*at:]))
	*at += 2
	if *at+n > len(b) {
		return "", io.ErrUnexpectedEOF
	}
	s := string(b[*at : *at+n])
	*at += n
	return s, nil
}

func (p *Conn) writeOpen(key []byte, open Open) error {
	b := make([]byte, 0, 128)
	b = append(b, proxyMagic...)
	b = binary.LittleEndian.AppendUint64(b, uint64(time.Now().Unix()))
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	b = append(b, nonce[:]...)
	b = binary.LittleEndian.AppendUint64(b, uint64(open.SinceSeq))
	b = binary.LittleEndian.AppendUint64(b, open.SinceTick)
	var err error
	for _, s := range []string{open.Sub, open.Park, open.Role, open.Remote} {
		b, err = appendProxyString(b, s)
		if err != nil {
			return err
		}
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	b = append(b, mac.Sum(nil)...)
	if len(b) > 65535 {
		return errors.New("proxy open too large")
	}
	p.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer p.SetWriteDeadline(time.Time{})
	var size [2]byte
	binary.LittleEndian.PutUint16(size[:], uint16(len(b)))
	if _, err := p.Write(size[:]); err != nil {
		return err
	}
	_, err = p.Write(b)
	return err
}

func (p *Conn) readOpen(key []byte, now time.Time) (Open, error) {
	p.SetReadDeadline(now.Add(5 * time.Second))
	defer p.SetReadDeadline(time.Time{})
	var size [2]byte
	if _, err := io.ReadFull(p, size[:]); err != nil {
		return Open{}, err
	}
	n := int(binary.LittleEndian.Uint16(size[:]))
	if n < len(proxyMagic)+8+16+8+8+32 || n > 65535 {
		return Open{}, errors.New("bad proxy open size")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(p, b); err != nil {
		return Open{}, err
	}
	body, sig := b[:len(b)-sha256.Size], b[len(b)-sha256.Size:]
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Open{}, errors.New("bad proxy signature")
	}
	if string(body[:len(proxyMagic)]) != proxyMagic {
		return Open{}, errors.New("bad proxy magic")
	}
	at := len(proxyMagic)
	ts := int64(binary.LittleEndian.Uint64(body[at:]))
	at += 8 + 16
	if d := now.Sub(time.Unix(ts, 0)); d < -30*time.Second || d > 30*time.Second {
		return Open{}, errors.New("stale proxy open")
	}
	open := Open{
		SinceSeq:  int64(binary.LittleEndian.Uint64(body[at:])),
		SinceTick: binary.LittleEndian.Uint64(body[at+8:]),
	}
	at += 16
	fields := []*string{&open.Sub, &open.Park, &open.Role, &open.Remote}
	for _, field := range fields {
		s, err := takeProxyString(body, &at)
		if err != nil {
			return Open{}, err
		}
		*field = s
	}
	if at != len(body) || open.Sub == "" || open.Park == "" || (open.Role != "player" && open.Role != "spectator") {
		return Open{}, errors.New("bad proxy open")
	}
	return open, nil
}

func (p *Conn) WriteMessage(kind byte, payload []byte) error {
	if len(payload) > proxyMaxPayload {
		return errors.New("proxy payload too large")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.SetWriteDeadline(time.Now().Add(10 * time.Second))
	defer p.SetWriteDeadline(time.Time{})
	var head [5]byte
	binary.LittleEndian.PutUint32(head[:4], uint32(len(payload)+1))
	head[4] = kind
	if _, err := p.Write(head[:]); err != nil {
		return err
	}
	_, err := p.Write(payload)
	return err
}

func (p *Conn) ReadMessage() (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(p, head[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.LittleEndian.Uint32(head[:4]))
	if n < 1 || n > proxyMaxPayload+1 {
		return 0, nil, fmt.Errorf("bad proxy message size %d", n)
	}
	b := make([]byte, n-1)
	_, err := io.ReadFull(p, b)
	return head[4], b, err
}
