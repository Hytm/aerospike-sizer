package service

import (
	"context"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedPortsAllowTwoInstances(t *testing.T) {
	first, err := Listen(context.Background(), "127.0.0.1:0", true)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Listen(context.Background(), first.Addr().String(), true)
	if err != nil {
		t.Fatalf("a second shared listener must bind the same port: %v", err)
	}
	second.Close()
	if l, err := Listen(context.Background(), first.Addr().String(), false); err == nil {
		l.Close()
		t.Error("a server started by hand must not take the service's port")
	}
}

// The handover rests on macOS giving every new connection to the newest shared listener, and
// back to the survivor once that one closes.
func TestNewConnectionsGoToTheNewestListener(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("SO_REUSEPORT balances connections on other systems")
	}
	accepted := func(ln net.Listener) *atomic.Int32 {
		var n atomic.Int32
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				n.Add(1)
				c.Close()
			}
		}()
		return &n
	}
	dial := func(addr string, times int) {
		for range times {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
		}
	}
	settle := func(counters ...*atomic.Int32) int32 {
		deadline := time.Now().Add(2 * time.Second)
		for {
			total := int32(0)
			for _, c := range counters {
				total += c.Load()
			}
			if time.Now().After(deadline) {
				return total
			}
			time.Sleep(10 * time.Millisecond)
			if again := func() int32 {
				s := int32(0)
				for _, c := range counters {
					s += c.Load()
				}
				return s
			}(); again == total {
				return total
			}
		}
	}

	old, err := Listen(context.Background(), "127.0.0.1:0", true)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	oldCount := accepted(old)
	fresh, err := Listen(context.Background(), old.Addr().String(), true)
	if err != nil {
		t.Fatal(err)
	}
	freshCount := accepted(fresh)

	dial(old.Addr().String(), 20)
	if total := settle(oldCount, freshCount); total != 20 || freshCount.Load() != 20 {
		t.Errorf("with two listeners: %d connections to the old one, %d to the new one", oldCount.Load(), freshCount.Load())
	}
	fresh.Close()
	time.Sleep(50 * time.Millisecond)
	dial(old.Addr().String(), 10)
	if total := settle(oldCount, freshCount); total != 30 || oldCount.Load() != 10 {
		t.Errorf("after the new one closed: %d connections to the old one, %d to the new one", oldCount.Load(), freshCount.Load())
	}
}
