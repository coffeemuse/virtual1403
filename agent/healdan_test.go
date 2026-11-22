package main

// Copyright 2026 CoffeeMuse
//
// This file is part of virtual1403
// <https://github.com/racingmars/virtual1403>.
//
// virtual1403 is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// virtual1403 is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with virtual1403. If not, see <https://www.gnu.org/licenses/>.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHealdan plays the parts of Healdan's API that the agent uses.
type fakeHealdan struct {
	mu           sync.Mutex
	signInStatus int   // 0 means 201
	fileStatuses []int // consumed in order; 201 once it runs out
	signIns      int
	filings      int
	requests     int
	names        []string
	properties   []map[string]any
	bodies       [][]byte
	srv          *httptest.Server
}

func (f *fakeHealdan) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	switch {
	case r.URL.Path == "/api/v1/auth/tokens":
		f.signIns++
		if f.signInStatus != 0 {
			writeProblem(w, f.signInStatus, "sign-in-failed")
			return
		}
		writeJSON(w, 201, `{"token":"hd_x"}`)
	case strings.HasSuffix(r.URL.Path, "/form"):
		writeJSON(w, 200, `{"items":[
			{"name":"author","type":"user"},
			{"name":"docclass","type":"string"}]}`)
	case r.URL.Path == "/api/v1/users":
		writeJSON(w, 200, `{"items":[{"id":9,"username":"jsmith"}]}`)
	case r.URL.Path == "/api/v1/documents":
		f.filings++
		status := 201
		if len(f.fileStatuses) > 0 {
			status = f.fileStatuses[0]
			f.fileStatuses = f.fileStatuses[1:]
		}
		if status != 201 {
			io.Copy(io.Discard, r.Body)
			writeProblem(w, status, "refused")
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var doc struct {
			Name       string         `json:"name"`
			Properties map[string]any `json:"properties"`
		}
		json.Unmarshal([]byte(r.FormValue("document")), &doc)
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body, _ := io.ReadAll(file)
		f.names = append(f.names, doc.Name)
		f.properties = append(f.properties, doc.Properties)
		f.bodies = append(f.bodies, body)
		writeJSON(w, 201, `{"docNumber":4321}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeHealdan) counts() (signIns, filings, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signIns, f.filings, f.requests
}

func newFakeHealdan(t *testing.T) *fakeHealdan {
	f := &fakeHealdan{}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func testHandler(t *testing.T, url string,
	properties map[string]string) (*healdanOutputHandler, string) {

	spool := t.TempDir()
	h, err := newHealdanOutputHandler(HealdanConfig{
		URL: url, Username: "u", Password: "p", FolderID: 1234,
		SpoolDir: spool, Properties: properties,
	}, "default-green", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	return h, spool
}

func printJob(h *healdanOutputHandler, jobinfo string) {
	h.AddLine("HELLO", true)
	h.EndOfJob(jobinfo)
}

func spoolFiles(t *testing.T, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestFilesJob(t *testing.T) {
	f := newFakeHealdan(t)
	h, spool := testHandler(t, f.srv.URL,
		map[string]string{"docclass": "Printouts", "author": "jsmith"})
	printJob(h, "JOB00001_TEST")

	if len(f.names) != 1 {
		t.Fatalf("filings = %v", f.names)
	}
	re := regexp.MustCompile(`^v1403-JOB00001_TEST-\d{8}T\d{6}\.pdf$`)
	if !re.MatchString(f.names[0]) {
		t.Errorf("name = %q", f.names[0])
	}
	if !strings.HasPrefix(string(f.bodies[0]), "%PDF") {
		t.Errorf("body isn't a PDF")
	}
	want := map[string]any{"docclass": "Printouts",
		"author": map[string]any{"id": float64(9)}}
	if !reflect.DeepEqual(f.properties[0], want) {
		t.Errorf("properties = %v", f.properties[0])
	}
	if files := spoolFiles(t, spool); len(files) != 0 {
		t.Errorf("spool = %v", files)
	}
}

func TestEmptyJobinfoName(t *testing.T) {
	f := newFakeHealdan(t)
	h, _ := testHandler(t, f.srv.URL, nil)
	printJob(h, "")
	re := regexp.MustCompile(`^v1403-\d{8}T\d{6}\.pdf$`)
	if len(f.names) != 1 || !re.MatchString(f.names[0]) {
		t.Errorf("names = %v", f.names)
	}
}

func TestRetryAfter503(t *testing.T) {
	f := newFakeHealdan(t)
	f.fileStatuses = []int{503}
	h, spool := testHandler(t, f.srv.URL, nil)
	printJob(h, "A")
	if files := spoolFiles(t, spool); len(files) != 1 {
		t.Fatalf("spool = %v", files)
	}
	if left := h.drainSpool(); left != 0 {
		t.Errorf("left = %d", left)
	}
	if files := spoolFiles(t, spool); len(files) != 0 {
		t.Errorf("spool = %v", files)
	}
}

func TestResignInOn401(t *testing.T) {
	f := newFakeHealdan(t)
	f.fileStatuses = []int{401}
	h, spool := testHandler(t, f.srv.URL, nil)
	printJob(h, "A")
	signIns, filings, _ := f.counts()
	if signIns != 2 || filings != 2 {
		t.Errorf("signIns = %d, filings = %d", signIns, filings)
	}
	if files := spoolFiles(t, spool); len(files) != 0 {
		t.Errorf("spool = %v", files)
	}
}

func TestForbiddenStaysAndIsSkipped(t *testing.T) {
	f := newFakeHealdan(t)
	f.fileStatuses = []int{403}
	h, spool := testHandler(t, f.srv.URL, nil)
	printJob(h, "A")
	if left := h.drainSpool(); left != 1 {
		t.Errorf("left = %d", left)
	}
	if _, filings, _ := f.counts(); filings != 1 {
		t.Errorf("filings = %d", filings)
	}
	if files := spoolFiles(t, spool); len(files) != 1 {
		t.Errorf("spool = %v", files)
	}
}

func TestSignInFailureStopsUploads(t *testing.T) {
	f := newFakeHealdan(t)
	f.signInStatus = 401
	h, spool := testHandler(t, f.srv.URL, nil)
	printJob(h, "A")
	printJob(h, "B")
	h.drainSpool()
	h.drainSpool()
	signIns, filings, _ := f.counts()
	if signIns != 1 || filings != 0 {
		t.Errorf("signIns = %d, filings = %d", signIns, filings)
	}
	if files := spoolFiles(t, spool); len(files) != 2 {
		t.Errorf("spool = %v", files)
	}
}

func TestUnknownPropertyHalts(t *testing.T) {
	f := newFakeHealdan(t)
	h, spool := testHandler(t, f.srv.URL, map[string]string{"nosuch": "x"})
	printJob(h, "A")
	_, filings, before := f.counts()
	h.drainSpool()
	_, _, after := f.counts()
	if filings != 0 || after != before {
		t.Errorf("filings = %d, requests %d -> %d", filings, before, after)
	}
	if files := spoolFiles(t, spool); len(files) != 1 {
		t.Errorf("spool = %v", files)
	}
}

func TestStartWhileDown(t *testing.T) {
	// Reserve an address, with nothing listening on it yet.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	h, spool := testHandler(t, "http://"+addr, nil)
	printJob(h, "A")
	if files := spoolFiles(t, spool); len(files) != 1 {
		t.Fatalf("spool = %v", files)
	}

	// Healdan comes up at that address.
	f := &fakeHealdan{}
	l, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("couldn't take %s back: %v", addr, err)
	}
	srv := httptest.NewUnstartedServer(f)
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	defer srv.Close()

	if left := h.drainSpool(); left != 0 {
		t.Errorf("left = %d", left)
	}
	if _, filings, _ := f.counts(); filings != 1 {
		t.Errorf("filings = %d", filings)
	}
}

func TestSameSecondNames(t *testing.T) {
	f := newFakeHealdan(t)
	f.fileStatuses = []int{503, 503, 503, 503}
	h, spool := testHandler(t, f.srv.URL, nil)
	// Pin the clock so both jobs end in the same second.
	h.now = func() time.Time { return time.Unix(1700000000, 0) }
	printJob(h, "A")
	printJob(h, "A")
	files := spoolFiles(t, spool)
	if len(files) != 2 || files[0] == files[1] {
		t.Errorf("spool = %v", files)
	}
	for _, name := range files {
		if !strings.HasSuffix(name, ".pdf") {
			t.Errorf("spooled file %q doesn't end in .pdf", name)
		}
	}
}

func TestPartialFileIgnored(t *testing.T) {
	f := newFakeHealdan(t)
	h, spool := testHandler(t, f.srv.URL, nil)
	os.WriteFile(filepath.Join(spool, "x.pdf.part"), []byte("%PDF"), 0600)
	h.drainSpool()
	if _, filings, _ := f.counts(); filings != 0 {
		t.Errorf("filings = %d", filings)
	}
	if files := spoolFiles(t, spool); len(files) != 1 {
		t.Errorf("spool = %v", files)
	}
}

func TestSpoolIgnoresOtherFiles(t *testing.T) {
	f := newFakeHealdan(t)
	h, spool := testHandler(t, f.srv.URL, nil)
	os.WriteFile(filepath.Join(spool, ".DS_Store"), []byte("x"), 0600)
	os.Mkdir(filepath.Join(spool, "old.pdf"), 0700)
	if left := h.drainSpool(); left != 0 {
		t.Errorf("left = %d", left)
	}
	if _, filings, _ := f.counts(); filings != 0 {
		t.Errorf("filings = %d", filings)
	}
}

func TestOldestFirst(t *testing.T) {
	f := newFakeHealdan(t)
	h, spool := testHandler(t, f.srv.URL, nil)
	os.WriteFile(filepath.Join(spool, "a.pdf"), []byte("%PDF a"), 0600)
	os.WriteFile(filepath.Join(spool, "b.pdf"), []byte("%PDF b"), 0600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(spool, "b.pdf"), old, old)
	h.drainSpool()
	if !reflect.DeepEqual(f.names, []string{"b.pdf", "a.pdf"}) {
		t.Errorf("names = %v", f.names)
	}
}

func TestTickerRetries(t *testing.T) {
	saved := retryInterval
	retryInterval = 20 * time.Millisecond
	defer func() { retryInterval = saved }()

	f := newFakeHealdan(t)
	f.fileStatuses = []int{503}
	h, spool := testHandler(t, f.srv.URL, nil)
	printJob(h, "A")
	h.start()
	defer h.stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(spoolFiles(t, spool)) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("spool still holds %v", spoolFiles(t, spool))
}

func TestUnknownUserHalts(t *testing.T) {
	f := newFakeHealdan(t)
	h, spool := testHandler(t, f.srv.URL, map[string]string{"author": "nobody"})
	printJob(h, "A")
	_, filings, before := f.counts()
	h.drainSpool()
	_, _, after := f.counts()
	if filings != 0 || after != before {
		t.Errorf("filings = %d, requests %d -> %d", filings, before, after)
	}
	if files := spoolFiles(t, spool); len(files) != 1 {
		t.Errorf("spool = %v", files)
	}
}
