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
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// do sends a request, with our token if we have one, and decodes a 2xx
// JSON answer into out (when out isn't nil). Any other status comes back
// as a *healdanError.
func (c *healdanClient) do(method, path, contentType string, body io.Reader,
	out any) error {

	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
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
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
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
		return he
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
// doc #.
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

	var filed struct {
		DocNumber int64 `json:"docNumber"`
	}
	err = c.do(http.MethodPost, "/documents", mw.FormDataContentType(),
		&body, &filed)
	return filed.DocNumber, err
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")
