package auth

import (
	"strings"
	"testing"
)

const shadowFixture = `root:!::0:::::
alice:$6$Gx2pQr7sLm9tVw1z$nIdtw29mh8NZeWN5kYZQLBWb6sqwVSUcBmKfBiHmUkzZCjM2ZYEV6evPEf0UhG6fpFVTqemVFZDBMtL4bXs1..:20000:0:99999:7:::
md5user:$1$Gx2pQr7s$opjYoupjvr7izJe/0rfXm.:20000::::::
sha256user:$5$Gx2pQr7sLm9tVw1z$jBNH9GQQej7.XrDsrRqMwGyGvf.mID01G/09mfCvJTD:20000::::::
yescryptuser:$y$j9T$F5Jx5fExrKuJdljLajF05.$C4q47a36QmTNi956pByDcRsYT7BDdobe1mAz7TCnVqC:20000::::::
bcryptuser:$2b$05$abcdefghijklmnopqrstuuu89jZiMbK9pU9r8luRhg53..TAcHzfy:20000::::::
locked:!$6$Gx2pQr7sLm9tVw1z$nIdtw29mh8NZeWN5kYZQLBWb6sqwVSUcBmKfBiHmUkzZCjM2ZYEV6evPEf0UhG6fpFVTqemVFZDBMtL4bXs1..:20000::::::
nologin:*:20000::::::
expired:$6$Gx2pQr7sLm9tVw1z$nIdtw29mh8NZeWN5kYZQLBWb6sqwVSUcBmKfBiHmUkzZCjM2ZYEV6evPEf0UhG6fpFVTqemVFZDBMtL4bXs1..:20000:0:99999:7::1:

# comment line
`

func TestParseShadow(t *testing.T) {
	entries, err := ParseShadow(strings.NewReader(shadowFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 9 {
		t.Fatalf("got %d entries", len(entries))
	}
	if e := entries["alice"]; !strings.HasPrefix(e.Hash, "$6$") || e.Expire != -1 {
		t.Fatalf("alice = %+v", e)
	}
	if e := entries["expired"]; e.Expire != 1 {
		t.Fatalf("expired = %+v", e)
	}
	if e := entries["root"]; e.Hash != "!" {
		t.Fatalf("root = %+v", e)
	}
}

func TestParseShadowExpiry(t *testing.T) {
	const content = `empty:x:20000:0:99999:7:::
numeric:x:20000:0:99999:7::20500:
unset:x:20000:0:99999:7::-1:
garbage:x:20000:0:99999:7::abc:
float:x:20000:0:99999:7::1.5:
negative:x:20000:0:99999:7::-5:
`
	entries, err := ParseShadow(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"empty":    -1, // empty field: account never expires
		"numeric":  20500,
		"unset":    -1,
		"garbage":  0, // unparsable: fail closed, treat as already expired
		"float":    0,
		"negative": 0, // any negative value other than -1 is invalid
	}
	for name, expire := range want {
		if got := entries[name].Expire; got != expire {
			t.Errorf("%s: Expire = %d, want %d", name, got, expire)
		}
	}
}
