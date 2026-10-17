package service

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden unit files")

func TestLoadTokenMissingShortAndRotated(t *testing.T) {
	home := t.TempDir()
	tok, err := loadToken(home)
	if err != nil || len(tok) != 64 {
		t.Fatalf("new token: %q %v", tok, err)
	}
	if st, _ := os.Stat(filepath.Join(home, "token")); st.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %v", st.Mode())
	}
	again, _ := loadToken(home)
	if again != tok {
		t.Fatal("token should be kept across starts")
	}
	// a damaged or short token file is replaced, not trusted
	os.WriteFile(filepath.Join(home, "token"), []byte("short\n"), 0o600)
	replaced, err := loadToken(home)
	if err != nil || len(replaced) != 64 || replaced == tok {
		t.Fatalf("short token kept: %q %v", replaced, err)
	}
	// deleting the file rotates it
	os.Remove(filepath.Join(home, "token"))
	rotated, _ := loadToken(home)
	if rotated == replaced || len(rotated) != 64 {
		t.Fatal("token not rotated")
	}
	if readToken(t.TempDir()) != "" {
		t.Fatal("readToken must not create a token")
	}
}

// The login units are rendered from templates; a golden file catches an
// unintended change to what gets installed on users' machines.
func TestUnitFilesMatchGolden(t *testing.T) {
	cases := map[string]func() (string, error){
		"launchd.plist": func() (string, error) {
			return launchd{}.render("/opt/groundwork/bin/groundwork", "/Users/me/Library/Application Support/groundwork", "/opt/homebrew/bin:/usr/bin:/bin")
		},
		"systemd.service": func() (string, error) {
			return systemd{}.render("/home/me/.local/bin/groundwork", `/home/me/.config/groundwork "quoted" 100%`, "/home/me/.local/bin:/usr/bin")
		},
	}
	for name, render := range cases {
		got, err := render()
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join("testdata", name)
		if *update {
			os.WriteFile(p, []byte(got), 0o644)
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v (run with -update to create it)", name, err)
		}
		if got != string(want) {
			t.Errorf("%s differs from golden:\n%s", name, got)
		}
	}
	// escaping: a quote in the home path must not break either format
	plist, _ := launchd{}.render("/x", `/h "q" <b>`, "/p")
	if !strings.Contains(plist, "&quot;q&quot; &lt;b&gt;") {
		t.Fatalf("plist not XML-escaped: %s", plist)
	}
	unit, _ := systemd{}.render("/x", `/h "q" 100%`, "/p")
	if !strings.Contains(unit, `\"q\" 100%%`) {
		t.Fatalf("systemd value not quoted: %s", unit)
	}
}

// fakeTool puts a script named name on PATH that records its arguments to
// log and exits 0 (or with the text in failWith on stderr and exit 1).
func fakeTool(t *testing.T, dir, name, log string, failWith string) {
	t.Helper()
	body := "#!/bin/sh\necho \"$*\" >> " + log + "\n"
	if failWith != "" {
		body += "echo '" + failWith + "' >&2\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Install writes the unit under the user's home and drives launchctl;
// Uninstall removes it. launchctl is a fake on PATH, HOME is a temp dir.
func TestLaunchdInstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	fakeTool(t, bin, "launchctl", log, "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	withUnit(t, nil) // use the real launchd manager below, not the fake
	manager = func() svcManager { return launchd{} }

	data := t.TempDir()
	path, err := Install(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Join(home, "Library", "LaunchAgents")) || !strings.HasSuffix(path, launchdLabel+".plist") {
		t.Fatalf("unit path %s", path)
	}
	unit, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GROUNDWORK_HOME", data, "<string>serve</string>", "StandardOutPath"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q", want)
		}
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "bootstrap gui/") {
		t.Fatalf("launchctl not asked to load the unit: %s", calls)
	}
	if !Installed() {
		t.Fatal("Installed should see the unit")
	}
	if ok, err := StartInstalled(); !ok || err != nil {
		t.Fatalf("StartInstalled: %v %v", ok, err)
	}
	removed, err := Uninstall()
	if err != nil || removed != path {
		t.Fatalf("uninstall: %q %v", removed, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("unit still there")
	}
	calls, _ = os.ReadFile(log)
	if !strings.Contains(string(calls), "bootout gui/") {
		t.Fatalf("launchctl not asked to unload: %s", calls)
	}
	if _, err := Uninstall(); err == nil {
		t.Fatal("uninstall twice should say it isn't installed")
	}
}

func TestSystemdInstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	fakeTool(t, bin, "systemctl", log, "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	withUnit(t, nil)
	manager = func() svcManager { return systemd{} }

	data := t.TempDir()
	path, err := Install(data)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "groundwork.service" {
		t.Fatalf("unit path %s", path)
	}
	unit, _ := os.ReadFile(path)
	if !strings.Contains(string(unit), "GROUNDWORK_HOME="+data) || !strings.Contains(string(unit), "serve --no-open") {
		t.Fatalf("unit: %s", unit)
	}
	calls, _ := os.ReadFile(log)
	for _, want := range []string{"--user daemon-reload", "--user enable --now groundwork.service"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("systemctl not asked %q: %s", want, calls)
		}
	}
	if _, err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	if !strings.Contains(string(calls), "--user disable --now groundwork.service") {
		t.Fatalf("systemctl not asked to disable: %s", calls)
	}
}

// A failing launchctl is reported, not hidden, and leaves no unit behind
// pretending to be installed.
func TestInstallReportsAFailingServiceManager(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	fakeTool(t, bin, "launchctl", filepath.Join(bin, "calls.log"), "Bootstrap failed: 5: Input/output error")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	withUnit(t, nil)
	manager = func() svcManager { return launchd{} }
	_, err := Install(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("got %v", err)
	}
}

// fakeLaunchctl behaves like launchd tearing a job down: bootstrap fails
// with "Input/output error" for its first failBootstraps calls, and print
// says the job is loaded for its first loadedPrints calls (-1: always).
func fakeLaunchctl(t *testing.T, failBootstraps, loadedPrints int) (log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(bin, "calls.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$1" >> %[1]s
n=$(grep -c "^$1$" %[1]s)
case "$1" in
  bootstrap) [ "$n" -le %[2]d ] && { echo 'Bootstrap failed: 5: Input/output error' >&2; exit 5; } ;;
  print) [ %[3]d -lt 0 ] || [ "$n" -le %[3]d ] || { echo 'Could not find service' >&2; exit 113; } ;;
esac
exit 0
`, log, failBootstraps, loadedPrints)
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldPoll, oldTries := launchdPoll, launchdTries
	launchdPoll, launchdTries = time.Millisecond, 10
	t.Cleanup(func() { launchdPoll, launchdTries = oldPoll, oldTries })
	return log
}

func launchctlCalls(t *testing.T, log string) string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return strings.Join(strings.Fields(string(b)), " ")
}

// Right after a bootout, launchd refuses to bootstrap the job for a moment:
// start retries instead of giving up (and falling back to a background service).
func TestLaunchdStartRetriesWhileTheJobIsTornDown(t *testing.T) {
	log := fakeLaunchctl(t, 3, 0)
	if err := (launchd{}).start(); err != nil {
		t.Fatal(err)
	}
	if got := launchctlCalls(t, log); got != "bootstrap print bootstrap print bootstrap print bootstrap" {
		t.Fatalf("calls: %s", got)
	}
}

// A job that's loaded but not running is kickstarted.
func TestLaunchdStartKickstartsALoadedJob(t *testing.T) {
	log := fakeLaunchctl(t, 99, -1)
	if err := (launchd{}).start(); err != nil {
		t.Fatal(err)
	}
	if got := launchctlCalls(t, log); got != "bootstrap print kickstart" {
		t.Fatalf("calls: %s", got)
	}
}

// When launchd never takes the job, start says why.
func TestLaunchdStartGivesUpWithTheReason(t *testing.T) {
	fakeLaunchctl(t, 99, 0)
	err := (launchd{}).start()
	if err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("got %v", err)
	}
}

// stop returns once launchd has removed the job, so a start right after it works.
func TestLaunchdStopWaitsForTheJobToGo(t *testing.T) {
	log := fakeLaunchctl(t, 0, 2)
	if err := (launchd{}).stop(); err != nil {
		t.Fatal(err)
	}
	if got := launchctlCalls(t, log); got != "bootout print print print" {
		t.Fatalf("calls: %s", got)
	}
	fakeLaunchctl(t, 0, -1)
	if err := (launchd{}).stop(); err == nil {
		t.Fatal("stop returned while the job was still loaded")
	}
}
