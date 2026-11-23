package transport

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func quicPair(t *testing.T) (client, server *quic.Conn) {
	t.Helper()
	srvTLS, err := SelfSignedServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := quic.ListenAddr("127.0.0.1:0", srvTLS, QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accepted := make(chan *quic.Conn, 1)
	go func() {
		c, err := ln.Accept(ctx)
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	client, err = quic.DialAddr(ctx, ln.Addr().String(), InsecureClientTLS(), QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	server = <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		client.CloseWithError(0, "")
		server.CloseWithError(0, "")
	})
	return client, server
}

func TestMediaKeysMatchAcrossQUICPeers(t *testing.T) {
	client, server := quicPair(t)
	for _, suite := range []Suite{SuiteAES128GCM, SuiteChaCha20Poly1305} {
		ck, err := MediaKeysFromConn(client, suite)
		if err != nil {
			t.Fatal(err)
		}
		sk, err := MediaKeysFromConn(server, suite)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ck.ClientToServer.Key, sk.ClientToServer.Key) || ck.ClientToServer.IV != sk.ClientToServer.IV ||
			!bytes.Equal(ck.ServerToClient.Key, sk.ServerToClient.Key) || ck.ServerToClient.IV != sk.ServerToClient.IV {
			t.Fatalf("%v: peers derived different keys", suite)
		}
		if bytes.Equal(ck.ClientToServer.Key, ck.ServerToClient.Key) {
			t.Fatalf("%v: both directions share a key", suite)
		}
		if len(ck.ClientToServer.Key) != suite.keyLen() {
			t.Fatalf("%v: key len %d", suite, len(ck.ClientToServer.Key))
		}

		seal, _ := NewSealer(ck.ClientToServer)
		open, _ := NewOpener(sk.ClientToServer)
		pkt := sealCopy(seal, []byte("hello over raw udp"))
		if got, err := open.Open(pkt); err != nil || string(got) != "hello over raw udp" {
			t.Fatalf("%v: cross-peer open: %q, %v", suite, got, err)
		}
	}

	aes, _ := MediaKeysFromConn(client, SuiteAES128GCM)
	cha, _ := MediaKeysFromConn(client, SuiteChaCha20Poly1305)
	if bytes.Equal(aes.ClientToServer.Key, cha.ClientToServer.Key[:16]) {
		t.Fatal("suite is not bound into the exporter context")
	}
}

func TestMediaKeysDifferPerConnection(t *testing.T) {
	c1, _ := quicPair(t)
	c2, _ := quicPair(t)
	k1, _ := MediaKeysFromConn(c1, SuiteAES128GCM)
	k2, _ := MediaKeysFromConn(c2, SuiteAES128GCM)
	if bytes.Equal(k1.ClientToServer.Key, k2.ClientToServer.Key) {
		t.Fatal("two sessions derived the same media key")
	}
}
