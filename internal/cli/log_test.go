package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loremlabs/pony/internal/api"
)

// logServer answers the log routes as OnePony does. Each read takes the next
// of its pages (the last repeats), and it keeps what it was asked.
type logServer struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	pages []api.Log
	reads int
	paths []string
	fail  map[int]int // read number → status
}

func newLogServer(t *testing.T, pages ...api.Log) *logServer {
	ls := &logServer{t: t, pages: pages, fail: map[int]int{}}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		ls.paths = append(ls.paths, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/ponies/stranger/") {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": "no_such_pony", "message": "no such pony"}})
			return
		}
		i := ls.reads
		ls.reads++
		if status := ls.fail[i]; status != 0 {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]string{"code": "unavailable", "message": "unavailable"}})
			return
		}
		if i >= len(ls.pages) {
			i = len(ls.pages) - 1
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "events": ls.pages[i].Events, "next": ls.pages[i].Next})
	}))
	t.Cleanup(ls.srv.Close)
	return ls
}

func (ls *logServer) asked() []string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return append([]string{}, ls.paths...)
}

func ev(id, at, who, text string) api.Event {
	return api.Event{ID: id, At: at, Pony: "pepper", Who: who, Kind: "said", Text: text}
}

const today = "2026-11-05T14:30:00.000000Z"

func atNoon(t *testing.T) {
	t.Helper()
	old := timeNow
	timeNow = func() time.Time { return time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { timeNow = old })
}

func TestLogTime(t *testing.T) {
	atNoon(t)
	cases := map[string]string{
		"2026-11-05T14:30:01.123456Z": "14:30:01",
		"2026-11-04T09:05:00Z":        "Nov  4 09:05:00",
		"not a time":                  "not a time",
	}
	for at, want := range cases {
		if got := logTime(at); got != want {
			t.Errorf("%s: got %q, want %q", at, got, want)
		}
	}
}

func TestLog(t *testing.T) {
	atNoon(t)
	s := newLogServer(t, api.Log{Events: []api.Event{
		ev("card:c1:1", today, "you", "filed “Quotes” in Ready"),
		ev("card:c1:2", "2026-11-05T14:30:05.000000Z", "pepper", "started on “Quotes”"),
	}, Next: "2026-11-05T14:30:05.000000Z"})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, errOut := run("log", "pepper", "-n", "5")
	want := "14:30:00  you filed “Quotes” in Ready\n14:30:05  pepper started on “Quotes”\n"
	if code != ExitOK || out != want {
		t.Fatalf("exit %d, %q (stderr %q)", code, out, errOut)
	}
	if got := s.asked()[0]; got != "/api/cli/v1/ponies/pepper/log?limit=5" {
		t.Fatalf("asked %q", got)
	}
	noToken(t, out, errOut)
}

func TestLogEveryPony(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{ev("a", today, "pepper", "said: hi")}})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	if code, _, errOut := run("log"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := s.asked()[0]; got != "/api/cli/v1/log?limit="+strconv.Itoa(logDefault) {
		t.Fatalf("asked %q", got)
	}
}

func TestLogJSON(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{ev("a", today, "pepper", "said: hi")}})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, _ := run("log", "--json")
	var got []api.Event
	if code != ExitOK || json.Unmarshal([]byte(out), &got) != nil || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("exit %d, %q", code, out)
	}

	empty := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": empty.srv.URL})
	if code, out, _ := run("log", "--json"); code != ExitOK || strings.TrimSpace(out) != "[]" {
		t.Fatalf("no lines should be an empty list: exit %d, %q", code, out)
	}
}

func TestLogNothingYet(t *testing.T) {
	s := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, errOut := run("log", "pepper")
	if code != ExitOK || out != "" || !strings.Contains(errOut, "nothing to show yet") {
		t.Fatalf("exit %d, %q, %q", code, out, errOut)
	}
}

func TestLogNoSuchPony(t *testing.T) {
	s := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, _, errOut := run("log", "stranger")
	if code != ExitNotFound || !strings.Contains(errOut, `no pony "stranger"`) {
		t.Fatalf("exit %d: %q", code, errOut)
	}
}

func TestLogFlags(t *testing.T) {
	s := newLogServer(t, api.Log{})
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	for _, n := range []string{"0", "101", "-3"} {
		if code, _, _ := run("log", "-n", n); code != ExitUsage {
			t.Fatalf("-n %s: exit %d", n, code)
		}
	}
	if code, _, _ := run("log", "pepper", "juniper"); code != ExitUsage {
		t.Fatalf("two ponies: exit %d", code)
	}
}

// follow runs followLog from the first page until it has read n more times.
func follow(t *testing.T, s *logServer, stop int, args ...string) (lines []string, code int) {
	t.Helper()
	quick(t)
	c, err := api.New(s.srv.URL, testToken, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := c.ReadLog(ctx, "pepper", "", logDefault)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range 5000 {
			s.mu.Lock()
			done := s.reads >= stop
			s.mu.Unlock()
			if done {
				break
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	var mu sync.Mutex
	code = followLog(ctx, c, "pepper", first, func(e api.Event) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, e.ID)
	}, &strings.Builder{})
	return lines, code
}

func TestFollowPrintsEachLineOnce(t *testing.T) {
	a, b, c, d := ev("a", today, "p", "1"), ev("b", today, "p", "2"), ev("c", today, "p", "3"), ev("d", today, "p", "4")
	s := newLogServer(t,
		api.Log{Events: []api.Event{a, b}, Next: "t1"},
		api.Log{Events: []api.Event{b}, Next: "t1"},    // what's near the cursor comes again
		api.Log{Events: []api.Event{b, c}, Next: "t2"}, // a line arrives
		api.Log{Events: nil, Next: "t2"},               // nothing, and nothing forgotten
		api.Log{Events: []api.Event{c, d}, Next: "t3"}, // the line before it again, and a new one
		api.Log{Events: []api.Event{c, d}, Next: "t3"},
	)
	lines, code := follow(t, s, 8)
	if code != 130 {
		t.Fatalf("exit %d", code)
	}
	if got := strings.Join(lines, ""); got != "cd" {
		t.Fatalf("printed %q, want each new line once", got)
	}
	paths := s.asked()
	if !strings.Contains(paths[len(paths)-1], "after=t3") {
		t.Fatalf("it should read on from the cursor: %v", paths)
	}
}

// The lines before the ones shown must not come back as new ones.
func TestFollowRemembersMoreThanItShows(t *testing.T) {
	quick(t)
	atNoon(t)
	older, newest, fresh := ev("older", today, "p", "older"), ev("newest", today, "p", "newest"), ev("fresh", today, "p", "fresh")
	s := newLogServer(t,
		api.Log{Events: []api.Event{older, newest}, Next: "t1"},
		api.Log{Events: []api.Event{older, newest, fresh}, Next: "t2"}, // what's near the cursor comes again
		api.Log{Next: "t2"},
	)
	s.fail[2] = 401 // ends the follow
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": s.srv.URL})
	code, out, _ := run("log", "-f", "-n", "1", "pepper")
	if code != ExitAuth {
		t.Fatalf("exit %d", code)
	}
	if got := strings.ReplaceAll(out, "14:30:00  p ", ""); got != "newest\nfresh\n" {
		t.Fatalf("printed %q", got)
	}
	if got := s.asked()[0]; !strings.Contains(got, "limit="+strconv.Itoa(logMost)) {
		t.Fatalf("following should read all it can first: %q", got)
	}
}

func TestFollowKeepsTheCursorThroughAnEmptyRead(t *testing.T) {
	s := newLogServer(t, api.Log{Events: []api.Event{ev("a", today, "p", "1")}, Next: "t1"}, api.Log{})
	follow(t, s, 4)
	paths := s.asked()
	if !strings.Contains(paths[len(paths)-1], "after=t1") {
		t.Fatalf("the cursor was lost: %v", paths)
	}
}

func TestFollowRidesOutAFailure(t *testing.T) {
	x := ev("x", today, "p", "1")
	s := newLogServer(t, api.Log{Next: "t1"}, api.Log{Next: "t1"}, api.Log{Next: "t1"}, api.Log{Events: []api.Event{x}, Next: "t2"})
	s.fail[1], s.fail[2] = 503, 502
	lines, code := follow(t, s, 6)
	if code != 130 || strings.Join(lines, "") != "x" {
		t.Fatalf("exit %d, %v", code, lines)
	}
}

func TestFollowStopsWhenSignedOut(t *testing.T) {
	s := newLogServer(t, api.Log{Next: "t1"})
	s.fail[1] = 401
	if _, code := follow(t, s, 50); code != ExitAuth {
		t.Fatalf("exit %d", code)
	}
}

func TestFollowGivesUpOnADeadServer(t *testing.T) {
	s := newLogServer(t, api.Log{Next: "t1"})
	for i := 1; i < 20; i++ {
		s.fail[i] = 503
	}
	if _, code := follow(t, s, 50); code == 130 || code == ExitOK {
		t.Fatalf("exit %d: it should have given up", code)
	}
}

func TestHelpNamesLog(t *testing.T) {
	code, out, _ := run("help")
	if code != ExitOK || !strings.Contains(out, "log") {
		t.Fatalf("exit %d, %q", code, out)
	}
}
