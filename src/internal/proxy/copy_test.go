package proxy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"lsh-utproxy/internal/stats"
)

// tcpPair returns two connected TCP peers over loopback.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	c1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	return c1, r.c
}

const chunkLen = 4096

func fillSeq(seq int, b []byte) {
	for i := range b {
		b[i] = byte((seq*31 + i*7 + 1) % 251)
	}
}

// writeStream sends count tagged blocks and then a FIN.
func writeStream(w net.Conn, count int) error {
	body := make([]byte, chunkLen)
	hdr := make([]byte, 4)
	for seq := 0; seq < count; seq++ {
		binary.BigEndian.PutUint32(hdr, uint32(chunkLen))
		if _, err := w.Write(hdr); err != nil {
			return err
		}
		fillSeq(seq, body)
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	if tc, ok := w.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	return nil
}

func readStream(r net.Conn, count int) error {
	hdr := make([]byte, 4)
	body := make([]byte, chunkLen)
	for seq := 0; seq < count; seq++ {
		if _, err := io.ReadFull(r, hdr); err != nil {
			return fmt.Errorf("seq %d header: %w", seq, err)
		}
		n := int(binary.BigEndian.Uint32(hdr))
		if n != chunkLen {
			return fmt.Errorf("seq %d corrupted framing: got length %d", seq, n)
		}
		if _, err := io.ReadFull(r, body); err != nil {
			return fmt.Errorf("seq %d body: %w", seq, err)
		}
		for i := range body {
			want := byte((seq*31 + i*7 + 1) % 251)
			if body[i] != want {
				return fmt.Errorf("seq %d corrupted payload at byte %d: got %d want %d", seq, i, body[i], want)
			}
		}
	}
	return nil
}

// Both directions carry traffic at the same time. If the two copy goroutines
// share one buffer, the forwarded stream gets corrupted.
func TestCopyBidirIntegrity(t *testing.T) {
	const streams = 4
	const perStream = 64

	for s := 0; s < streams; s++ {
		cliA, cliP := tcpPair(t)
		beP, beA := tcpPair(t)

		var wg sync.WaitGroup
		errc := make(chan error, 4)

		wg.Add(1)
		go func() { // copy engine
			defer wg.Done()
			copyBidir(beP, cliP, stats.New(), copyTimeouts{clientIdle: 10 * time.Second, serverIdle: 10 * time.Second})
		}()

		wg.Add(1)
		go func() { // client -> backend
			defer wg.Done()
			if err := writeStream(cliA, perStream); err != nil {
				errc <- fmt.Errorf("client write: %w", err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := readStream(beA, perStream); err != nil {
				errc <- fmt.Errorf("client->backend: %w", err)
				_ = beA.Close()
			}
		}()

		wg.Add(1)
		go func() { // backend -> client
			defer wg.Done()
			if err := writeStream(beA, perStream); err != nil {
				errc <- fmt.Errorf("backend write: %w", err)
				_ = beA.Close()
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := readStream(cliA, perStream); err != nil {
				errc <- fmt.Errorf("backend->client: %w", err)
				_ = cliA.Close()
			}
		}()

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("stream %d: copy engine stalled", s)
		}

		close(errc)
		for err := range errc {
			t.Errorf("stream %d: %v", s, err)
		}
		_ = cliA.Close()
		_ = cliP.Close()
		_ = beA.Close()
		_ = beP.Close()
	}
}

// A hard error (RST) in one direction must end the session, not leave the
// other direction blocked until the inactivity timeout fires.
func TestCopyBidirHardErrorEndsSession(t *testing.T) {
	cliA, cliP := tcpPair(t)
	beA, beP := tcpPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		copyBidir(beP, cliP, stats.New(), copyTimeouts{clientIdle: time.Hour, serverIdle: time.Hour})
	}()

	// Unread data in the queue + SO_LINGER 0 => the peer gets RST.
	if tc, ok := beA.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	if _, err := beA.Write([]byte("ping")); err != nil {
		t.Fatalf("backend write: %v", err)
	}
	if err := beA.Close(); err != nil {
		t.Logf("backend close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("session still open 5s after backend reset: the other direction is not unblocked")
	}
	_ = cliA.Close()
	_ = cliP.Close()
	_ = beP.Close()
}

// A clean FIN on both sides must close the session: each direction half-closes
// its peer, and no timer has to expire.
func TestCopyBidirHalfClose(t *testing.T) {
	cliA, cliP := tcpPair(t)
	beA, beP := tcpPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		copyBidir(beP, cliP, stats.New(), copyTimeouts{clientIdle: time.Hour, serverIdle: time.Hour})
	}()

	if tc, ok := cliA.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	b := make([]byte, 16)
	_ = beA.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := beA.Read(b)
	if n != 0 {
		t.Fatalf("backend read %d bytes after client FIN", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("backend did not see FIN: err=%v", err)
	}
	if tc, ok := beA.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("session not closed after both sides sent FIN")
	}
	_ = cliA.Close()
	_ = cliP.Close()
	_ = beA.Close()
	_ = beP.Close()
}

// Each side has its own inactivity timer (HAProxy TCP behaviour): traffic in
// one direction must not keep the idle timer of the other direction alive.
func TestCopyBidirPerDirectionIdle(t *testing.T) {
	cliA, cliP := tcpPair(t)
	beA, beP := tcpPair(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		copyBidir(beP, cliP, stats.New(), copyTimeouts{
			clientIdle: 10 * time.Second,
			serverIdle: 1500 * time.Millisecond,
		})
	}()

	go func() {
		body := make([]byte, 512)
		for i := 0; i < 100; i++ {
			if _, err := cliA.Write(body); err != nil {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
	}()

	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatalf("session survived although the backend direction was idle the whole time")
	}
	_ = cliA.Close()
	_ = cliP.Close()
	_ = beA.Close()
	_ = beP.Close()
}
