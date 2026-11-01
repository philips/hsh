package workspace

import (
	"strings"
	"testing"
)

func TestParseListeners(t *testing.T) {
	v4 := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F40 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 0200570A:A4D2 0100570A:1F90 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
   2: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0000000000000000 100 0 0 10 0
`
	got, err := ParseListeners(strings.NewReader(v4), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Listener{8000, "127.0.0.1"}) || got[1] != (Listener{80, "0.0.0.0"}) {
		t.Fatalf("v4 = %+v", got)
	}
	v6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
`
	got, err = ParseListeners(strings.NewReader(v6), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Listener{8080, "::1"}) || got[1] != (Listener{3000, "::"}) {
		t.Fatalf("v6 = %+v", got)
	}
}

func TestParseListenersMalformed(t *testing.T) {
	bad := "header\n 0: ZZ:1F40 00000000:0000 0A\n"
	if _, err := ParseListeners(strings.NewReader(bad), false); err == nil {
		t.Fatal("want error for malformed address")
	}
}
