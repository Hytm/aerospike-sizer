package service

import (
	"context"
	"testing"
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
