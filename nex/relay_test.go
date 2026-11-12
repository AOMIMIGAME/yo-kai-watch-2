package nex

import (
	"net"
	"testing"
	"time"
)

const relayTestFirstPort = 41100

func testClient(t *testing.T) *net.UDPConn {
	t.Helper()
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	return socket
}

func localAddr(port int) []*net.UDPAddr {
	return []*net.UDPAddr{{IP: net.IPv4(127, 0, 0, 1), Port: port}}
}

func relayAddr(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
}

func readPacket(t *testing.T, socket *net.UDPConn) (string, *net.UDPAddr) {
	t.Helper()
	buffer := make([]byte, 256)
	socket.SetReadDeadline(time.Now().Add(2 * time.Second))
	size, from, err := socket.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("no packet: %v", err)
	}
	return string(buffer[:size]), from
}

func expectNoPacket(t *testing.T, socket *net.UDPConn) {
	t.Helper()
	buffer := make([]byte, 256)
	socket.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := socket.ReadFromUDP(buffer); err == nil {
		t.Fatal("unexpected packet")
	}
}

func TestRelayForwardsBothWays(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort, 4)
	one, two := testClient(t), testClient(t)
	s1, err := relay.assign(1, localAddr(one.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := relay.assign(2, localAddr(two.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}

	// * 1 が 2 の中継ポートへ送ると、2 には 1 の中継ポートから届く
	one.WriteToUDP([]byte("hello"), relayAddr(s2.port))
	data, from := readPacket(t, two)
	if data != "hello" || from.Port != s1.port {
		t.Fatalf("%s from %v", data, from)
	}
	two.WriteToUDP([]byte("reply"), relayAddr(s1.port))
	data, from = readPacket(t, one)
	if data != "reply" || from.Port != s2.port {
		t.Fatalf("%s from %v", data, from)
	}
}

// * NAT で、URL のポートと実際の送信元のポートが違う場合
func TestRelayLearnsRealPort(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort+10, 4)
	one, two := testClient(t), testClient(t)
	s1, _ := relay.assign(1, localAddr(1)) // * URL のポートは本当の送信元と違う
	s2, _ := relay.assign(2, localAddr(2))
	relay.setPartners(1, 2)

	one.WriteToUDP([]byte("a"), relayAddr(s2.port))
	time.Sleep(100 * time.Millisecond)
	two.WriteToUDP([]byte("b"), relayAddr(s1.port))
	data, _ := readPacket(t, one)
	if data != "b" {
		t.Fatal(data)
	}
	one.WriteToUDP([]byte("c"), relayAddr(s2.port))
	data, _ = readPacket(t, two)
	if data != "c" {
		t.Fatal(data)
	}
}

func TestRelayDropsUnknownSender(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort+20, 4)
	stranger := testClient(t)
	two := testClient(t)
	other := []*net.UDPAddr{{IP: net.IPv4(10, 9, 8, 7), Port: 1234}}
	relay.assign(1, other)
	s2, _ := relay.assign(2, localAddr(two.LocalAddr().(*net.UDPAddr).Port))

	// * 127.0.0.1 は 2 自身の IP で、1 の IP ではない。知らない送信元は相手へ届けない
	stranger.WriteToUDP([]byte("x"), relayAddr(s2.port))
	expectNoPacket(t, two)
}

func TestRelayReusesPorts(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort+30, 2)
	relay.assign(1, localAddr(1))
	s2, err := relay.assign(2, localAddr(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.assign(3, localAddr(3)); err == nil {
		t.Fatal("expected no ports left")
	}

	// * 同じ PID はポートを増やさない
	again, err := relay.assign(2, localAddr(22))
	if err != nil || again.port != s2.port {
		t.Fatalf("%v %v", again, err)
	}

	// * 使っている間は返さない
	relay.releaseIdle(time.Now())
	if _, err := relay.assign(3, localAddr(3)); err == nil {
		t.Fatal("active ports must not be released")
	}

	// * 時間がたてば返して、ほかの人が使える
	relay.releaseIdle(time.Now().Add(relayIdleLimit + time.Minute))
	if len(relay.byPID) != 0 || len(relay.freePorts) != 2 {
		t.Fatalf("%d %d", len(relay.byPID), len(relay.freePorts))
	}
	s3, err := relay.assign(3, localAddr(3))
	if err != nil {
		t.Fatal(err)
	}
	if s3.port < relayTestFirstPort+30 || s3.port >= relayTestFirstPort+32 {
		t.Fatal(s3.port)
	}
}

func TestRelayReleasesSoonAfterBattle(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort+50, 4)
	one, two := testClient(t), testClient(t)
	relay.assign(1, localAddr(one.LocalAddr().(*net.UDPAddr).Port))
	s2, _ := relay.assign(2, localAddr(two.LocalAddr().(*net.UDPAddr).Port))
	relay.assign(3, localAddr(3)) // * 相手を探している人

	one.WriteToUDP([]byte("battle"), relayAddr(s2.port))
	readPacket(t, two)

	// * 対戦の直後は返さない。通信が止まって少しすれば返す。相手を探している人は残す
	relay.releaseIdle(time.Now().Add(time.Minute))
	if len(relay.byPID) != 3 {
		t.Fatalf("released too early: %d", len(relay.byPID))
	}
	relay.releaseIdle(time.Now().Add(relayDoneLimit + time.Minute))
	if _, ok := relay.byPID[3]; !ok || len(relay.byPID) != 1 {
		t.Fatalf("after battle: %d", len(relay.byPID))
	}

	// * 次に相手を探せば、また用意する
	again, err := relay.assign(1, localAddr(1))
	if err != nil || again.sawPackets {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestRelayKeepsSearchersUntilTheyLeave(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort+60, 4)
	online := map[uint32]bool{1: true, 2: true}
	relay.connected = func(pid uint32) bool { return online[pid] }
	relay.assign(1, localAddr(1))
	relay.assign(2, localAddr(2))

	// * つながっている間は、30 分探していても返さない
	relay.releaseIdle(time.Now().Add(30 * time.Minute))
	if len(relay.byPID) != 2 {
		t.Fatalf("searcher released: %d", len(relay.byPID))
	}

	// * サーバーから切れた人の分はすぐ返す
	online[2] = false
	relay.releaseIdle(time.Now())
	if _, ok := relay.byPID[2]; ok || len(relay.byPID) != 1 {
		t.Fatalf("disconnected searcher kept: %d", len(relay.byPID))
	}

	// * 探すのをやめた人の分も、いずれ返す
	relay.releaseIdle(time.Now().Add(relayIdleLimit + time.Minute))
	if len(relay.byPID) != 0 {
		t.Fatalf("idle kept: %d", len(relay.byPID))
	}
}

func TestRelayManyPairsStaySeparate(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort+40, 8)
	a1, a2, b1, b2 := testClient(t), testClient(t), testClient(t), testClient(t)
	portOf := func(c *net.UDPConn) int { return c.LocalAddr().(*net.UDPAddr).Port }
	s1, _ := relay.assign(1, localAddr(portOf(a1)))
	s2, _ := relay.assign(2, localAddr(portOf(a2)))
	s3, _ := relay.assign(3, localAddr(portOf(b1)))
	s4, _ := relay.assign(4, localAddr(portOf(b2)))
	relay.setPartners(1, 2)
	relay.setPartners(3, 4)
	_ = s1
	_ = s3

	a1.WriteToUDP([]byte("for two"), relayAddr(s2.port))
	data, _ := readPacket(t, a2)
	if data != "for two" {
		t.Fatal(data)
	}
	b1.WriteToUDP([]byte("for four"), relayAddr(s4.port))
	data, _ = readPacket(t, b2)
	if data != "for four" {
		t.Fatal(data)
	}
	expectNoPacket(t, a2)
}
