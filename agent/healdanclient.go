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
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// healdanError is any answer from Healdan that isn't a success. Code is
// the `code` member of Healdan's problem document, or "" when the body
// isn't one (a proxy's error page, for example).
type healdanError struct {
	Status int
	Code   string
	Detail string
}

func (e *healdanError) Error() string {
	switch {
	case e.Code != "" && e.Detail != "":
		return fmt.Sprintf("Healdan answered %d %s: %s", e.Status, e.Code,
			e.Detail)
	case e.Code != "":
		return fmt.Sprintf("Healdan answered %d %s", e.Status, e.Code)
	case e.Detail != "":
		return fmt.Sprintf("Healdan answered %d: %s", e.Status, e.Detail)
	default:
		return fmt.Sprintf("Healdan answered %d", e.Status)
	}
}

// noSuchUserError means Healdan answered, and has no user of that name.
type noSuchUserError struct {
	username string
}

func (e *noSuchUserError) Error() string {
	return fmt.Sprintf("Healdan has no active user named `%s`", e.username)
}

// healdanField is one property of a folder's filing form. We only need to
// know its name and whether it holds a user (as Author and Typist do).
type healdanField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// healdanClient talks to Healdan's JSON API under <base>/api/v1.
type healdanClient struct {
	base  string
	http  *http.Client
	token string
}

func newHealdanClient(baseURL string) *healdanClient {
	return &healdanClient{
		base: strings.TrimRight(baseURL, "/") + "/api/v1",
		http: &http.Client{
			Timeout: 60 * time.Second,
			// Healdan's API never redirects. Something in front of it
			// might (to a login page, say), and following that would
			// turn a POST into a GET of some other page.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// do sends a request, with our token if we have one, and decodes a 2xx
// JSON answer into out (when out isn't nil). Any other status comes back
// as a *healdanError.
func (c *healdanClient) do(method, path, contentType string, body io.Reader,
	out any) error {

	resp, err := c.send(method, path, contentType, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// send sends a request, with our token if we have one, and returns the
// answer if its status is 2xx. The caller closes its body. Any other
// status comes back as a *healdanError.
func (c *healdanClient) send(method, path, contentType string,
	body io.Reader) (*http.Response, error) {

	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		he := &healdanError{Status: resp.StatusCode}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var problem struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &problem) == nil {
			he.Code = problem.Code
			he.Detail = problem.Detail
		}
		return nil, he
	}
	return resp, nil
}

// signIn gets a device token with a username and password, and keeps it
// for the requests that follow.
func (c *healdanClient) signIn(username, password, deviceName string) error {
	body, err := json.Marshal(map[string]string{
		"username": username,
		"password": password,
		"name":     deviceName,
	})
	if err != nil {
		return err
	}

	// A sign-in never carries the old token.
	c.token = ""
	var signedIn struct {
		Token string `json:"token"`
	}
	if err := c.do(http.MethodPost, "/auth/tokens", "application/json",
		bytes.NewReader(body), &signedIn); err != nil {
		return err
	}
	if signedIn.Token == "" {
		return fmt.Errorf("Healdan's sign-in answer had no token")
	}
	c.token = signedIn.Token
	return nil
}

// folderForm returns the properties a document filed into the folder can
// have.
func (c *healdanClient) folderForm(folderID int64) ([]healdanField, error) {
	var page struct {
		Items []healdanField `json:"items"`
	}
	err := c.do(http.MethodGet,
		"/folders/"+strconv.FormatInt(folderID, 10)+"/form", "", nil, &page)
	return page.Items, err
}

// findUserID returns the id of the user with exactly this username,
// ignoring case.
func (c *healdanClient) findUserID(username string) (int64, error) {
	var page struct {
		Items []struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	err := c.do(http.MethodGet, "/users?q="+url.QueryEscape(username), "",
		nil, &page)
	if err != nil {
		return 0, err
	}
	for _, hit := range page.Items {
		if strings.EqualFold(hit.Username, username) {
			return hit.ID, nil
		}
	}
	return 0, &noSuchUserError{username}
}

// fileDocument files a PDF as a new document in the folder and returns its
// doc #, or 0 when it was filed but Healdan's answer didn't give one.
func (c *healdanClient) fileDocument(folderID int64, name string,
	properties map[string]any, pdf io.Reader) (int64, error) {

	document := map[string]any{"folderId": folderID, "name": name}
	if len(properties) > 0 {
		document["properties"] = properties
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		return 0, err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="document"`)
	h.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(h)
	if err != nil {
		return 0, err
	}
	if _, err := part.Write(documentJSON); err != nil {
		return 0, err
	}

	h = make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(
		`form-data; name="file"; filename="%s"`, quoteEscaper.Replace(name)))
	h.Set("Content-Type", "application/pdf")
	part, err = mw.CreatePart(h)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(part, pdf); err != nil {
		return 0, err
	}
	if err := mw.Close(); err != nil {
		return 0, err
	}

	resp, err := c.send(http.MethodPost, "/documents",
		mw.FormDataContentType(), &body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// Healdan answers 201 once it holds the document. Any other success
	// came from something else, and doesn't mean the document is filed.
	if resp.StatusCode != http.StatusCreated {
		return 0, &healdanError{Status: resp.StatusCode,
			Detail: "expected 201 for a filed document"}
	}

	// The document is filed whatever the body says: an answer we can't
	// read must not make the caller file it again.
	var filed struct {
		DocNumber int64 `json:"docNumber"`
	}
	if json.NewDecoder(resp.Body).Decode(&filed) != nil {
		return 0, nil
	}
	return filed.DocNumber, nil
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")
