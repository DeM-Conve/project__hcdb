package resp

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestReadSimpleString(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("+OK\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != SimpleString || v.Str != "OK" {
		t.Fatalf("got %+v", v)
	}
}

func TestReadError(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("-ERR bad thing\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != Error || v.Str != "ERR bad thing" {
		t.Fatalf("got %+v", v)
	}
}

func TestReadInteger(t *testing.T) {
	v, err := Read(bufio.NewReader(strings(":1000\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != Integer || v.Num != 1000 {
		t.Fatalf("got %+v", v)
	}
}

func TestReadNegativeInteger(t *testing.T) {
	v, err := Read(bufio.NewReader(strings(":-5\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Num != -5 {
		t.Fatalf("got %+v", v)
	}
}

func TestReadBulkString(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("$6\r\nfoobar\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != BulkString || v.Str != "foobar" {
		t.Fatalf("got %+v", v)
	}
}

func TestReadEmptyBulkString(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("$0\r\n\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Null || v.Str != "" {
		t.Fatalf("got %+v", v)
	}
}

func TestReadNullBulkString(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("$-1\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != BulkString || !v.Null {
		t.Fatalf("got %+v", v)
	}
}

// TestReadBulkStringWithEmbeddedCRLF is the case that forces length-prefixed
// reading in the first place: the content itself contains \r\n, so a
// delimiter-based read would stop early and corrupt the value.
func TestReadBulkStringWithEmbeddedCRLF(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("$6\r\nab\r\ncd\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Str != "ab\r\ncd" {
		t.Fatalf("got %q, want %q", v.Str, "ab\r\ncd")
	}
}

func TestReadArray(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("*2\r\n$3\r\nfoo\r\n$3\r\nbar\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != Array || len(v.Elems) != 2 {
		t.Fatalf("got %+v", v)
	}
	if v.Elems[0].Str != "foo" || v.Elems[1].Str != "bar" {
		t.Fatalf("got %+v", v)
	}
}

func TestReadNullArray(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("*-1\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if v.Type != Array || !v.Null {
		t.Fatalf("got %+v", v)
	}
}

func TestReadNestedArray(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("*1\r\n*2\r\n:1\r\n:2\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Elems) != 1 || len(v.Elems[0].Elems) != 2 {
		t.Fatalf("got %+v", v)
	}
	if v.Elems[0].Elems[0].Num != 1 || v.Elems[0].Elems[1].Num != 2 {
		t.Fatalf("got %+v", v)
	}
}

func TestReadCommandArray(t *testing.T) {
	v, err := Read(bufio.NewReader(strings("*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SET", "foo", "bar"}
	if len(v.Elems) != 3 {
		t.Fatalf("got %+v", v)
	}
	for i, w := range want {
		if v.Elems[i].Str != w {
			t.Fatalf("elem %d = %q, want %q", i, v.Elems[i].Str, w)
		}
	}
}

func TestReadUnknownTagByte(t *testing.T) {
	_, err := Read(bufio.NewReader(strings("@garbage\r\n")))
	if err == nil {
		t.Fatal("expected an error for an unknown tag byte, got nil")
	}
}

// TestChunkedRead is the case the curriculum specifically asks for: a client
// that writes one byte per underlying Read call must parse identically to
// one that writes the whole command at once.
func TestChunkedRead(t *testing.T) {
	payload := "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"
	v, err := Read(bufio.NewReader(&oneByteAtATime{data: []byte(payload)}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SET", "foo", "bar"}
	if len(v.Elems) != 3 {
		t.Fatalf("got %+v", v)
	}
	for i, w := range want {
		if v.Elems[i].Str != w {
			t.Fatalf("elem %d = %q, want %q", i, v.Elems[i].Str, w)
		}
	}
}

// TestChunkedReadOverRealConn repeats the same case over an actual TCP
// socket with writes paced one byte at a time, so the chunking is enforced
// by the OS/network stack, not just a test double.
func TestChunkedReadOverRealConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	payload := []byte("*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n")

	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for _, b := range payload {
			conn.Write([]byte{b})
			time.Sleep(time.Millisecond)
		}
		close(done)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	v, err := Read(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Elems) != 3 || v.Elems[0].Str != "SET" {
		t.Fatalf("got %+v", v)
	}
	<-done
}

func TestWriteRoundTrip(t *testing.T) {
	cases := []Value{
		SimpleStringValue("OK"),
		ErrorValue("ERR nope"),
		IntegerValue(42),
		IntegerValue(-1),
		BulkStringValue("hello"),
		BulkStringValue(""),
		NullBulkStringValue(),
		ArrayValue(BulkStringValue("a"), BulkStringValue("b")),
		NullArrayValue(),
		ArrayValue(),
	}
	for _, in := range cases {
		var buf bytes.Buffer
		w := bufio.NewWriter(&buf)
		if err := Write(w, in); err != nil {
			t.Fatalf("write %+v: %v", in, err)
		}
		out, err := Read(bufio.NewReader(&buf))
		if err != nil {
			t.Fatalf("read back %+v: %v", in, err)
		}
		if !equalValue(in, out) {
			t.Fatalf("round trip mismatch: in=%+v out=%+v", in, out)
		}
	}
}

func equalValue(a, b Value) bool {
	if a.Type != b.Type || a.Str != b.Str || a.Num != b.Num || a.Null != b.Null {
		return false
	}
	if len(a.Elems) != len(b.Elems) {
		return false
	}
	for i := range a.Elems {
		if !equalValue(a.Elems[i], b.Elems[i]) {
			return false
		}
	}
	return true
}

func strings(s string) io.Reader { return bytes.NewReader([]byte(s)) }

// oneByteAtATime forces bufio.Reader to make many short underlying reads.
type oneByteAtATime struct {
	data []byte
	pos  int
}

func (o *oneByteAtATime) Read(p []byte) (int, error) {
	if o.pos >= len(o.data) {
		return 0, io.EOF
	}
	p[0] = o.data[o.pos]
	o.pos++
	return 1, nil
}
