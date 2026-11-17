package forward

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestConnectionLimitIncludesPendingTargets(t *testing.T) {
	f := New("127.0.0.1:0", "127.0.0.1:1")
	f.MaxConnections = 1
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.IsReady = func() bool {
		once.Do(func() { close(entered) })
		<-release
		return false
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	defer close(release)
	first, err := net.Dial("tcp", f.BoundAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first pipe did not start")
	}
	second, err := net.Dial("tcp", f.BoundAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, err = second.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("connection over limit remained open")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("pending target was not counted toward the connection limit")
	}
}

func TestOneWayTrafficKeepsPipeAlive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 20; i++ {
			if _, err := conn.Write([]byte{byte(i)}); err != nil {
				return
			}
			time.Sleep(40 * time.Millisecond)
		}
	}()
	f := New("127.0.0.1:0", ln.Addr().String())
	f.IdleTimeout = 300 * time.Millisecond
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { f.Stop(); <-done }()
	client, err := net.Dial("tcp", f.BoundAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetReadDeadline(time.Now().Add(3 * time.Second))
	data := make([]byte, 20)
	if n, err := io.ReadFull(client, data); err != nil {
		t.Fatalf("active download closed after %d bytes: %v", n, err)
	}
	for i, b := range data {
		if int(b) != i {
			t.Fatalf("corrupted byte %d: %d", i, b)
		}
	}
}

func TestInactiveAndHalfClosedPipesExpire(t *testing.T) {
	for _, halfClose := range []bool{false, true} {
		name := "idle"
		if halfClose {
			name = "half_closed"
		}
		t.Run(name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				c, err := ln.Accept()
				if err == nil {
					accepted <- c
				}
			}()
			f := New("127.0.0.1:0", ln.Addr().String())
			f.IdleTimeout = 200 * time.Millisecond
			f.HalfCloseTimeout = 100 * time.Millisecond
			if err := f.Start(); err != nil {
				t.Fatal(err)
			}
			defer f.Stop()
			client, err := net.Dial("tcp", f.BoundAddr())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			select {
			case target := <-accepted:
				defer target.Close()
			case <-time.After(time.Second):
				t.Fatal("target was not connected")
			}
			if halfClose {
				client.(*net.TCPConn).CloseWrite()
			}
			client.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, err = client.Read(make([]byte, 1))
			if err == nil {
				t.Fatal("expected closed connection")
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("forwarder did not close inactive pipe")
			}
		})
	}
}
