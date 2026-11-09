package selfupdate

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHealthURL(t *testing.T) {
	for _, c := range []struct {
		listen   string
		tls      bool
		url      string
		insecure bool
	}{
		{"0.0.0.0:8080", false, "http://127.0.0.1:8080/api/health", false},
		{":8080", false, "http://127.0.0.1:8080/api/health", false},
		{"[::]:8080", false, "http://[::1]:8080/api/health", false},
		{"127.0.0.1:9000", false, "http://127.0.0.1:9000/api/health", false},
		{"192.168.1.5:443", true, "https://192.168.1.5:443/api/health", true},
		{"[::1]:8443", true, "https://[::1]:8443/api/health", true},
		{"0.0.0.0:8443", true, "https://127.0.0.1:8443/api/health", true},
		{"localhost:8080", false, "http://localhost:8080/api/health", false},
		{"[fe80::1]:8080", false, "http://[fe80::1]:8080/api/health", false},
		{"[fe80::1%eth0]:8080", false, "http://[fe80::1%25eth0]:8080/api/health", false},
	} {
		u, insecure, err := HealthURL(c.listen, c.tls)
		if err != nil || u != c.url || insecure != c.insecure {
			t.Errorf("HealthURL(%q, %v) = %q, %v, %v; want %q, %v", c.listen, c.tls, u, insecure, err, c.url, c.insecure)
		}
	}
	for _, listen := range []string{"", "localhost", "127.0.0.1", "[::1]", "host:", "a b:80"} {
		if u, _, err := HealthURL(listen, false); err == nil {
			t.Errorf("HealthURL(%q) = %q, want an error", listen, u)
		}
	}
}

func testPlan(dir string) Plan {
	return Plan{
		Binary: "/usr/local/bin/edelweissd", Staged: "/usr/local/bin/edelweissd.edelweiss-new",
		BinaryBackup: "/usr/local/bin/edelweissd.edelweiss-bak", InitScript: "/etc/init.d/edelweiss",
		InitBackup: filepath.Join(dir, "init.edelweiss-bak"), NewScript: filepath.Join(dir, "init.new"),
		From: "0.3.0", To: "0.3.1", HealthURL: "https://127.0.0.1:8443/api/health", Insecure: true, Lbu: true,
		RcService: "/sbin/rc-service", LbuTool: "/sbin/lbu", Wait: 60 * time.Second, Stable: 30 * time.Second, Dir: dir,
		Script: "#!/sbin/openrc-run\n",
	}
}

func TestWritePlanRoundTrip(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state", "self-update")
	p := testPlan(dir)
	path, err := WritePlan(p)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "plan.json") {
		t.Errorf("path = %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Plan
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := p
	want.Script = "" // the script is in init.new, not in the plan
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %+v, want %+v", got, want)
	}
	if s, err := os.ReadFile(p.NewScript); err != nil || string(s) != p.Script {
		t.Errorf("init.new = %q, %v", s, err)
	}
	for _, f := range []string{path, p.NewScript} {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", f, fi, err)
		}
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v, %v", fi, err)
	}
}

func TestWritePlanChecksItsPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "self-update")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := testPlan(dir)
	p.NewScript = filepath.Join(t.TempDir(), "init.new")
	if _, err := WritePlan(p); err == nil {
		t.Error("WritePlan accepted an init.new outside the plan directory")
	}
	if _, err := os.Stat(p.NewScript); err == nil {
		t.Error("init.new was written")
	}
	p = testPlan(dir)
	if _, err := WritePlan(p); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("an existing directory stays loose: %v, %v", fi, err)
	}
}

func TestReadStateRunning(t *testing.T) {
	dir := t.TempDir()
	if st, err := ReadState(dir); err != nil || st.Running || st.Result != nil {
		t.Fatalf("empty dir: %+v, %v", st, err)
	}
	if st, err := ReadState(filepath.Join(dir, "missing")); err != nil || st.Running || st.Result != nil {
		t.Fatalf("missing dir: %+v, %v", st, err)
	}
	lock := filepath.Join(dir, "lock")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), testModeVar+"=holdlock", testLockVar+"="+lock)
	stdin, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child: %q, %v", line, err)
	}
	if st, err := ReadState(dir); err != nil || !st.Running {
		t.Errorf("while the child holds the lock: %+v, %v", st, err)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if st, err := ReadState(dir); err != nil || st.Running {
		t.Errorf("after the child ended: %+v, %v", st, err)
	}
	// the probe must not leave the lock held
	if st, err := ReadState(dir); err != nil || st.Running {
		t.Errorf("second probe: %+v, %v", st, err)
	}
}

func TestReadStateUnreadableResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	st, err := ReadState(dir)
	if err != nil || st.Result == nil {
		t.Fatalf("ReadState = %+v, %v", st, err)
	}
	if r := *st.Result; r.Outcome != "failed" || r.Reason != "result_unreadable" || !r.At.Equal(mtime) {
		t.Errorf("Result = %+v", r)
	}
	if err := Dismiss(dir); err != nil {
		t.Fatal(err)
	}
	if st, err := ReadState(dir); err != nil || st.Result != nil {
		t.Errorf("after Dismiss: %+v, %v", st, err)
	}
}

func TestDismiss(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "self-update")
	if err := Dismiss(dir); err != nil {
		t.Errorf("Dismiss without a result = %v", err)
	}
	r := Result{From: "0.3.0", To: "0.3.1", Outcome: "rolled_back", Reason: "not_healthy", At: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	if err := WriteResult(dir, r); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "result.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("result.json: %v, %v", fi, err)
	}
	st, err := ReadState(dir)
	if err != nil || st.Result == nil || !reflect.DeepEqual(*st.Result, r) {
		t.Fatalf("ReadState = %+v, %v", st, err)
	}
	if err := Dismiss(dir); err != nil {
		t.Fatal(err)
	}
	if st, err := ReadState(dir); err != nil || st.Result != nil {
		t.Errorf("after Dismiss: %+v, %v", st, err)
	}
	js, _ := json.Marshal(State{Running: true})
	if string(js) != `{"running":true}` {
		t.Errorf("State JSON = %s", js)
	}
}
