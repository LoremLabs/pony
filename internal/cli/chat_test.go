package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loremlabs/pony/internal/api"
)

// quick makes the waiting loops fast for a test.
func quick(t *testing.T) {
	t.Helper()
	oldFast, oldSlow, oldFor, oldRetry := pollFast, pollSlow, fastFor, api.Retry
	pollFast, pollSlow, fastFor = time.Millisecond, time.Millisecond, time.Hour
	api.Retry = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { pollFast, pollSlow, fastFor, api.Retry = oldFast, oldSlow, oldFor, oldRetry })
}

// step is one read of a card: its state, and the lines it has by then.
type step struct {
	state string
	lines []api.Line
}

// chatServer answers the chat and card routes as OnePony does. A line goes
// on the card the client named, at seq 2; each read of a card takes the
// next of its steps (the last repeats). The first POST of each key fails
// with 502 when flaky is set, as a line kept whose pony couldn't be woken.
type chatServer struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	steps  []step
	reads  int
	flaky  bool
	posted []map[string]string
	keys   []string
}

func newChatServer(t *testing.T, steps ...step) *chatServer {
	cs := &chatServer{t: t, steps: steps}
	answer := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, status int, code string) {
		answer(w, status, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": strings.ReplaceAll(code, "_", " ")}})
	}
	chatPath := regexp.MustCompile(`^/api/cli/v1/ponies/([^/]+)/chat$`)
	cardPath := regexp.MustCompile(`^/api/cli/v1/cards/([^/]+)$`)
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			fail(w, 401, "not_signed_in")
			return
		}
		if m := chatPath.FindStringSubmatch(r.URL.Path); m != nil && r.Method == http.MethodPost {
			switch m[1] {
			case "stranger":
				fail(w, 404, "no_such_pony")
				return
			case "juniper":
				fail(w, 409, "pony_waiting")
				return
			}
			var body map[string]string
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &body)
			key := r.Header.Get("Idempotency-Key")
			cs.posted = append(cs.posted, body)
			cs.keys = append(cs.keys, key)
			if cs.flaky && len(cs.keys) == 1 {
				answer(w, 502, map[string]any{"ok": false, "error": map[string]string{"code": "chat_failed"}, "code": "chat_failed"})
				return
			}
			answer(w, 200, map[string]any{"ok": true, "intent": "working", "say": []string{}, "card": body["card"], "seq": 2, "at": "2026-11-04T10:00:00Z"})
			return
		}
		if m := cardPath.FindStringSubmatch(r.URL.Path); m != nil && r.Method == http.MethodGet {
			if m[1] == "wch_nope0000" {
				fail(w, 404, "no_such_card")
				return
			}
			after, _ := strconv.Atoi(r.URL.Query().Get("after"))
			s := cs.steps[min(cs.reads, len(cs.steps)-1)]
			cs.reads++
			lines := []api.Line{}
			next := after
			for _, l := range s.lines {
				if l.Seq > after {
					lines = append(lines, l)
					next = max(next, l.Seq)
				}
			}
			answer(w, 200, map[string]any{"ok": true, "card": map[string]string{"id": m[1], "pony": "pepper", "state": s.state}, "lines": lines, "next": next})
			return
		}
		if r.URL.Path == "/api/cli/v1/ponies/pepper" {
			answer(w, 200, map[string]any{"ok": true, "pony": map[string]any{"slug": "pepper", "name": "Pepper", "address": "pepper@mail.example", "status": "working", "doing": "Taxes", "ready": true, "card": "wch_tax00001"}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/cli/v1/ponies/") {
			fail(w, 404, "no_such_pony")
			return
		}
		fail(w, 404, "not_found")
	}))
	t.Cleanup(cs.srv.Close)
	quick(t)
	withEnv(t, map[string]string{"ONEPONY_TOKEN": testToken, "ONEPONY_SERVER": cs.srv.URL})
	return cs
}

var (
	mine      = api.Line{Seq: 2, From: "you", Text: "What is 2+2?"}
	heard     = api.Line{Seq: 3, From: "pony", Text: "Got it. Give me a moment."}
	answered  = api.Line{Seq: 5, From: "pony", Text: "4."}
	elsewhere = api.Line{Seq: 4, From: "you", Text: "typed in the browser meanwhile"}
)

func TestAsk(t *testing.T) {
	cs := newChatServer(t,
		step{"queued", []api.Line{mine}},
		step{"working", []api.Line{mine, heard}},
		step{"working", []api.Line{mine, heard, elsewhere}},
		step{"idle", []api.Line{mine, heard, elsewhere, answered}},
	)
	cs.flaky = true
	code, out, errOut := run("ask", "pepper", "What", "is", "2+2?")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if out != "Got it. Give me a moment.\n4.\n" {
		t.Fatalf("stdout %q: the pony's lines, as they came, and nothing of anyone else's", out)
	}
	if len(cs.posted) != 2 || cs.keys[0] == "" || cs.keys[0] != cs.keys[1] {
		t.Fatalf("a line that failed is sent again under the same key: %v", cs.keys)
	}
	if cs.posted[0]["text"] != "What is 2+2?" || !regexp.MustCompile(`^wch_[a-z0-9]{8,32}$`).MatchString(cs.posted[0]["card"]) || cs.posted[1]["card"] != cs.posted[0]["card"] {
		t.Fatalf("posted %v: the words joined, on a fresh chat of its own", cs.posted)
	}
	noToken(t, out, errOut)
}

func TestAskJSONAndFlagsAnywhere(t *testing.T) {
	newChatServer(t, step{"idle", []api.Line{mine, answered}})
	code, out, errOut := run("ask", "pepper", "What is 2+2?", "--json")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var v struct {
		Pony, Card, State string
		Lines             []api.Line
	}
	if json.Unmarshal([]byte(out), &v) != nil || v.Pony != "pepper" || v.State != "idle" || len(v.Lines) != 1 || v.Lines[0].Text != "4." || !strings.HasPrefix(v.Card, "wch_") {
		t.Fatalf("--json after the words: %q", out)
	}
}

func TestAskCarriesOnAChat(t *testing.T) {
	cs := newChatServer(t, step{"idle", []api.Line{mine, answered}})
	if code, _, errOut := run("ask", "--card", "wch_abcd1234", "pepper", "and 3+3?"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if cs.posted[0]["card"] != "wch_abcd1234" {
		t.Fatalf("posted %v", cs.posted)
	}
	if code, _, _ := run("ask", "--card", "a/b", "pepper", "hi"); code != ExitUsage {
		t.Fatalf("a card id of the wrong shape: exit %d", code)
	}
}

func TestAskStdin(t *testing.T) {
	cs := newChatServer(t, step{"idle", []api.Line{mine, answered}})
	old := stdin
	t.Cleanup(func() { stdin = old })

	stdin = strings.NewReader("  Summarize this:\nline one\n")
	if code, _, errOut := run("ask", "pepper", "-"); code != ExitOK || cs.posted[0]["text"] != "Summarize this:\nline one" {
		t.Fatalf("exit %d, posted %v: %s", code, cs.posted, errOut)
	}
	for name, in := range map[string]string{
		"empty":    " \n",
		"too long": strings.Repeat("é", maxLine+1),
		"not text": "\xff\xfe",
	} {
		stdin = strings.NewReader(in)
		if code, _, errOut := run("ask", "pepper", "-"); code != ExitUsage {
			t.Fatalf("%s: exit %d, %s", name, code, errOut)
		}
	}
	if code, _, _ := run("ask", "pepper"); code != ExitUsage {
		t.Fatalf("no words: exit %d", code)
	}
	if len(cs.posted) != 1 {
		t.Fatalf("nothing refused here was sent: %v", cs.posted)
	}
}

func TestAskWaitsOnYou(t *testing.T) {
	held := api.Line{Seq: 6, From: "pony", Text: "I'd like to send a mail to v@else.test. It's waiting for your go-ahead on the board."}
	newChatServer(t, step{"working", []api.Line{mine}}, step{"needs_you", []api.Line{mine, held}})
	code, out, errOut := run("ask", "pepper", "mail the vendor")
	if code != ExitWaiting || out != held.Text+"\n" || !strings.Contains(errOut, "waiting on you") || !strings.Contains(errOut, "/board?card=wch_") || !strings.Contains(errOut, "pony watch wch_") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestAskOutcomes(t *testing.T) {
	newChatServer(t, step{"failed", []api.Line{mine}})
	if code, _, errOut := run("ask", "pepper", "hi"); code != ExitFailed || !strings.Contains(errOut, "failed") {
		t.Fatalf("failed run: exit %d, %q", code, errOut)
	}

	newChatServer(t, step{"idle", []api.Line{mine}})
	if code, _, errOut := run("ask", "pepper", "hi"); code != ExitOK || !strings.Contains(errOut, "finished without a reply") {
		t.Fatalf("no reply: exit %d, %q", code, errOut)
	}

	newChatServer(t, step{"working", []api.Line{mine, heard}})
	code, out, errOut := run("ask", "--timeout", "30ms", "pepper", "research the market")
	if code != ExitTimeout || out != heard.Text+"\n" || !strings.Contains(errOut, "carries on") || !strings.Contains(errOut, "pony watch wch_") {
		t.Fatalf("timeout: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestAskRefused(t *testing.T) {
	newChatServer(t, step{"idle", nil})
	if code, _, errOut := run("ask", "stranger", "hi"); code != ExitNotFound || !strings.Contains(errOut, "no pony") {
		t.Fatalf("not reached: exit %d, %q", code, errOut)
	}
	if code, _, errOut := run("ask", "juniper", "hi"); code != ExitWaiting || !strings.Contains(errOut, "getting ready") {
		t.Fatalf("not let in yet: exit %d, %q", code, errOut)
	}
	withEnv(t, map[string]string{"ONEPONY_TOKEN": "", "ONEPONY_SERVER": "http://localhost:1"})
	if code, _, _ := run("ask", "pepper", "hi"); code != ExitAuth {
		t.Fatalf("signed out: exit %d", code)
	}
}

func TestWatch(t *testing.T) {
	newChatServer(t,
		step{"working", []api.Line{mine}},
		step{"working", []api.Line{mine, heard}},
		step{"idle", []api.Line{mine, heard, answered}},
	)
	code, out, errOut := run("watch", "wch_abcd1234")
	if code != ExitOK || out != "you: What is 2+2?\npepper: Got it. Give me a moment.\npepper: 4.\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	newChatServer(t, step{"working", []api.Line{mine}}, step{"needs_you", []api.Line{mine, heard}})
	code, out, _ = run("watch", "--json", "--after", "2", "wch_abcd1234")
	var kinds []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var v map[string]any
		if json.Unmarshal([]byte(l), &v) != nil {
			t.Fatalf("not NDJSON: %q", out)
		}
		kinds = append(kinds, v["type"].(string)+":"+func() string {
			if s, ok := v["state"].(string); ok {
				return s
			}
			return v["text"].(string)
		}())
	}
	if code != ExitWaiting || strings.Join(kinds, "|") != "state:working|line:Got it. Give me a moment.|state:needs_you" {
		t.Fatalf("exit %d, events %v", code, kinds)
	}

	// A card already answered: its pony is named on the first read's lines too.
	newChatServer(t, step{"idle", []api.Line{mine, answered}})
	if code, out, _ := run("watch", "wch_abcd1234"); code != ExitOK || out != "you: What is 2+2?\npepper: 4.\n" {
		t.Fatalf("an answered card: exit %d, %q", code, out)
	}

	if code, _, _ := run("watch", "wch_nope0000"); code != ExitNotFound {
		t.Fatalf("not a card it reaches: exit %d", code)
	}
	if code, _, _ := run("watch"); code != ExitUsage {
		t.Fatalf("no card: exit %d", code)
	}
}

func TestStatus(t *testing.T) {
	newChatServer(t, step{"idle", nil})
	code, out, errOut := run("status", "pepper")
	if code != ExitOK || out != "Pepper <pepper@mail.example>\nworking: Taxes\ncard wch_tax00001 (pony watch wch_tax00001)\n" {
		t.Fatalf("exit %d, %q, %q", code, out, errOut)
	}
	code, out, _ = run("status", "pepper", "--json")
	var p api.Pony
	if code != ExitOK || json.Unmarshal([]byte(out), &p) != nil || p.Card != "wch_tax00001" {
		t.Fatalf("--json: exit %d, %q", code, out)
	}
	if code, _, _ := run("status", "stranger"); code != ExitNotFound {
		t.Fatalf("not reached: exit %d", code)
	}
}
