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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func writeProblem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"type": "https://healdan.com/problems/" + code, "title": code,
		"status": status, "detail": "detail of " + code, "code": code,
	})
}

func TestSignIn(t *testing.T) {
	var formAuth string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/auth/tokens":
				if r.Method != http.MethodPost {
					t.Errorf("method = %s", r.Method)
				}
				if r.Header.Get("Authorization") != "" {
					t.Errorf("sign-in sent an Authorization header")
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				want := map[string]any{"username": "u", "password": "p",
					"name": "virtual1403 test"}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("body = %v", body)
				}
				writeJSON(w, 201, `{"token":"hd_x","id":1}`)
			case "/api/v1/folders/5/form":
				formAuth = r.Header.Get("Authorization")
				writeJSON(w, 200, `{"items":[]}`)
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		}))
	defer srv.Close()

	c := newHealdanClient(srv.URL)
	if err := c.signIn("u", "p", "virtual1403 test"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.folderForm(5); err != nil {
		t.Fatal(err)
	}
	if formAuth != "Bearer hd_x" {
		t.Errorf("Authorization = %q", formAuth)
	}
}

func TestSignInFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, 401, "sign-in-failed")
		}))
	defer srv.Close()

	err := newHealdanClient(srv.URL).signIn("u", "bad", "x")
	var he *healdanError
	if !errors.As(err, &he) {
		t.Fatalf("err = %v", err)
	}
	if he.Status != 401 || he.Code != "sign-in-failed" ||
		he.Detail != "detail of sign-in-failed" {
		t.Errorf("error = %+v", he)
	}
}

func TestFolderForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `{"items":[
				{"name":"author","type":"user","label":"Author"},
				{"name":"docclass","type":"string","label":"Class"}],
				"offset":0,"limit":2,"total":2}`)
		}))
	defer srv.Close()

	fields, err := newHealdanClient(srv.URL).folderForm(5)
	if err != nil {
		t.Fatal(err)
	}
	want := []healdanField{{Name: "author", Type: "user"},
		{Name: "docclass", Type: "string"}}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("fields = %+v", fields)
	}
}

func TestFindUserID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/users" ||
				r.URL.Query().Get("q") != "jsmith" {
				t.Errorf("request = %s", r.URL)
			}
			writeJSON(w, 200, `{"items":[
				{"id":7,"username":"jsmithers","name":"J Smithers"},
				{"id":9,"username":"JSmith","name":"J Smith"}]}`)
		}))
	defer srv.Close()

	c := newHealdanClient(srv.URL)
	id, err := c.findUserID("jsmith")
	if err != nil || id != 9 {
		t.Errorf("id = %d, err = %v", id, err)
	}
}

func TestFindUserIDNoMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200,
				`{"items":[{"id":7,"username":"jsmithers","name":"x"}]}`)
		}))
	defer srv.Close()

	_, err := newHealdanClient(srv.URL).findUserID("jsmith")
	if err == nil || !strings.Contains(err.Error(), "jsmith") {
		t.Errorf("err = %v", err)
	}
}

func TestFileDocument(t *testing.T) {
	pdf := []byte("%PDF-1.4 fake")
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/documents" ||
				r.Method != http.MethodPost {
				t.Errorf("request = %s %s", r.Method, r.URL)
			}
			mr, err := r.MultipartReader()
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(part)
				seen[part.FormName()] = true
				switch part.FormName() {
				case "document":
					if ct := part.Header.Get("Content-Type"); ct !=
						"application/json" {
						t.Errorf("document Content-Type = %q", ct)
					}
					var doc map[string]any
					json.Unmarshal(data, &doc)
					want := map[string]any{
						"folderId": float64(1234), "name": "a.pdf",
						"properties": map[string]any{
							"docclass": "Printouts",
							"author":   map[string]any{"id": float64(9)},
						}}
					if !reflect.DeepEqual(doc, want) {
						t.Errorf("document = %v", doc)
					}
				case "file":
					if part.FileName() != "a.pdf" {
						t.Errorf("filename = %q", part.FileName())
					}
					if !bytes.Equal(data, pdf) {
						t.Errorf("file bytes differ")
					}
				}
			}
			if !seen["document"] || !seen["file"] {
				t.Errorf("parts = %v", seen)
			}
			writeJSON(w, 201, `{"docNumber":4321,"name":"a.pdf"}`)
		}))
	defer srv.Close()

	n, err := newHealdanClient(srv.URL).fileDocument(1234, "a.pdf",
		map[string]any{"docclass": "Printouts",
			"author": map[string]any{"id": int64(9)}},
		bytes.NewReader(pdf))
	if err != nil || n != 4321 {
		t.Errorf("docNumber = %d, err = %v", n, err)
	}
}

func TestNonProblemError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(502)
			io.WriteString(w, "<html>Bad Gateway</html>")
		}))
	defer srv.Close()

	_, err := newHealdanClient(srv.URL).folderForm(5)
	var he *healdanError
	if !errors.As(err, &he) || he.Status != 502 || he.Code != "" {
		t.Errorf("err = %v", err)
	}
}

func TestBaseURLTrailingSlash(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			writeJSON(w, 201, `{"token":"hd_x"}`)
		}))
	defer srv.Close()

	if err := newHealdanClient(srv.URL+"/").signIn("u", "p", "x"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/auth/tokens" {
		t.Errorf("path = %q", path)
	}
}
