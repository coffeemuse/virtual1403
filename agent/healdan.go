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
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/racingmars/virtual1403/vprinter"
)

// retryInterval is how often PDFs waiting in the spool are tried again.
var retryInterval = time.Minute

// healdanOutputHandler files each job's PDF in a folder of a Healdan
// document management system. Every PDF is first written to a spool
// directory and only removed once Healdan has confirmed it holds the
// document, so a job survives Healdan being down or refusing it.
type healdanOutputHandler struct {
	job       vprinter.Job
	conf      HealdanConfig
	profile   string
	font      []byte
	inputName string
	now       func() time.Time

	// mu guards everything below, and makes sure only one upload runs at
	// a time (a job ending and the retry timer may both want to).
	mu         sync.Mutex
	client     *healdanClient
	ready      bool            // signed in, properties resolved
	halted     bool            // no uploads until the agent is restarted
	properties map[string]any  // as Healdan takes them
	failed     map[string]bool // files Healdan refused; not retried
	stopCh     chan struct{}
	stopped    chan struct{}
}

type uploadResult int

const (
	uploadDone   uploadResult = iota // filed; the spooled file is gone
	uploadRetry                      // try again later
	uploadFailed                     // refused; needs a person
)

func newHealdanOutputHandler(conf HealdanConfig, profile string,
	fontOverride []byte, inputName string) (*healdanOutputHandler, error) {

	h := &healdanOutputHandler{
		conf:      conf,
		profile:   profile,
		font:      fontOverride,
		inputName: inputName,
		now:       time.Now,
		client:    newHealdanClient(conf.URL),
		failed:    make(map[string]bool),
	}

	if err := os.MkdirAll(conf.SpoolDir, 0700); err != nil {
		return nil, fmt.Errorf("couldn't create spool directory `%s`: %v",
			conf.SpoolDir, err)
	}

	var err error
	h.job, err = vprinter.NewProfile(profile, fontOverride, 11.4)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (h *healdanOutputHandler) AddLine(line string, linefeed bool) {
	h.job.AddLine(line, linefeed)
}

func (h *healdanOutputHandler) PageBreak() {
	h.job.NewPage()
}

func (h *healdanOutputHandler) EndOfJob(jobinfo string) {
	// No matter what happens, we always want to reset our state to a fresh
	// new job.
	defer func() {
		var err error
		h.job, err = vprinter.NewProfile(h.profile, h.font, 11.4)
		if err != nil {
			log.Printf("ERROR: [%s] couldn't re-initialize virtual 1403: %v",
				h.inputName, err)
			log.Printf(
				"ERROR: [%s] application is probably in a bad state, "+
					"please restart.", h.inputName)
		}
	}()

	h.mu.Lock()
	defer h.mu.Unlock()

	name, pages, err := h.spoolJob(jobinfo)
	if err != nil {
		log.Printf("ERROR: [%s] couldn't write PDF to the spool: %v",
			h.inputName, err)
		return
	}
	log.Printf("INFO:  [%s] wrote %d page PDF to the spool as %s",
		h.inputName, pages, name)

	h.upload(name)
}

// spoolJob writes the current job's PDF into the spool directory and
// returns its file name there. The PDF is written under a ".part" name
// and renamed once complete, so a half-written file is never uploaded.
func (h *healdanOutputHandler) spoolJob(jobinfo string) (string, int,
	error) {

	if jobinfo != "" {
		jobinfo = jobinfo + "-"
	}
	base := fmt.Sprintf("v1403-%s%s", jobinfo,
		h.now().UTC().Format("20060102T150405"))

	// Two jobs can end in the same second with the same job info.
	name := base + ".pdf"
	for n := 2; h.spoolNameTaken(name); n++ {
		name = fmt.Sprintf("%s-%d.pdf", base, n)
	}

	final := filepath.Join(h.conf.SpoolDir, name)
	partial := final + ".part"

	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", 0, err
	}
	pages, err := h.job.EndJob(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(partial, final)
	}
	if err != nil {
		os.Remove(partial)
		return "", 0, err
	}
	return name, pages, nil
}

func (h *healdanOutputHandler) spoolNameTaken(name string) bool {
	path := filepath.Join(h.conf.SpoolDir, name)
	for _, p := range []string{path, path + ".part"} {
		if _, err := os.Lstat(p); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// start begins retrying the spool on a timer.
func (h *healdanOutputHandler) start() {
	h.stopCh = make(chan struct{})
	h.stopped = make(chan struct{})
	go func() {
		defer close(h.stopped)
		ticker := time.NewTicker(retryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-h.stopCh:
				return
			case <-ticker.C:
				h.drainSpool()
			}
		}
	}()
}

// stop ends the retry timer and waits for a retry in progress to finish.
func (h *healdanOutputHandler) stop() {
	if h.stopCh == nil {
		return
	}
	close(h.stopCh)
	<-h.stopped
	h.stopCh = nil
}

// drainSpool tries once to file every PDF waiting in the spool, oldest
// first, and returns how many are still there afterwards.
func (h *healdanOutputHandler) drainSpool() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	waiting, err := h.spooled()
	if err != nil {
		log.Printf("ERROR: [%s] couldn't read spool directory `%s`: %v",
			h.inputName, h.conf.SpoolDir, err)
		return 0
	}

	left := len(waiting)
	for _, name := range waiting {
		if h.halted {
			break
		}
		if h.failed[name] {
			continue
		}
		result := h.upload(name)
		if result == uploadDone {
			left--
		}
		if result == uploadRetry {
			// Healdan isn't answering as it should; the rest can wait
			// for the next pass.
			break
		}
	}
	return left
}

// spooled lists the complete PDFs in the spool, oldest first.
func (h *healdanOutputHandler) spooled() ([]string, error) {
	entries, err := os.ReadDir(h.conf.SpoolDir)
	if err != nil {
		return nil, err
	}

	type spooledFile struct {
		name    string
		modTime time.Time
	}
	var files []spooledFile
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".pdf") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // gone since we listed it
		}
		files = append(files, spooledFile{e.Name(), info.ModTime()})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if !files[i].modTime.Equal(files[j].modTime) {
			return files[i].modTime.Before(files[j].modTime)
		}
		return files[i].name < files[j].name
	})

	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.name
	}
	return names, nil
}

// upload files one spooled PDF in Healdan. The caller holds h.mu.
func (h *healdanOutputHandler) upload(name string) uploadResult {
	if !h.ensureReady() {
		return uploadRetry
	}

	docNumber, err := h.fileSpooled(name)
	if isStatus(err, 401) {
		// Our token stopped working (they lapse, and are revoked when
		// the password changes). Sign in again and try once more.
		h.ready = false
		if !h.ensureReady() {
			return uploadRetry
		}
		docNumber, err = h.fileSpooled(name)
	}

	if err == nil {
		path := filepath.Join(h.conf.SpoolDir, name)
		if rerr := os.Remove(path); rerr != nil {
			// Don't file it a second time on the next pass.
			h.failed[name] = true
			log.Printf("ERROR: [%s] filed %s in Healdan as doc #%d, but "+
				"couldn't remove it from the spool: %v", h.inputName, name,
				docNumber, rerr)
			return uploadFailed
		}
		log.Printf("INFO:  [%s] filed %s in Healdan as doc #%d",
			h.inputName, name, docNumber)
		return uploadDone
	}

	if retryable(err) {
		log.Printf("WARN:  [%s] couldn't file %s in Healdan, will try "+
			"again: %v", h.inputName, name, err)
		return uploadRetry
	}

	h.failed[name] = true
	log.Printf("ERROR: [%s] Healdan refused %s: %v", h.inputName, name, err)
	log.Printf("ERROR: [%s] %s stays in `%s`; it will be tried again when "+
		"the agent is restarted", h.inputName, name, h.conf.SpoolDir)
	return uploadFailed
}

func (h *healdanOutputHandler) fileSpooled(name string) (int64, error) {
	f, err := os.Open(filepath.Join(h.conf.SpoolDir, name))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return h.client.fileDocument(h.conf.FolderID, name, h.properties, f)
}

// ensureReady signs in and works out the properties to send, if that
// hasn't been done yet. It returns false when uploads can't go ahead: for
// now (Healdan can't be reached) or until a restart (h.halted). The caller
// holds h.mu.
func (h *healdanOutputHandler) ensureReady() bool {
	if h.halted {
		return false
	}
	if h.ready {
		return true
	}

	err := h.client.signIn(h.conf.Username, h.conf.Password,
		"virtual1403 "+h.inputName)
	if err != nil {
		if retryable(err) {
			log.Printf("WARN:  [%s] couldn't sign in to Healdan at %s, "+
				"will try again: %v", h.inputName, h.conf.URL, err)
			return false
		}
		// Never try a refused sign-in again by ourselves: a few wrong
		// passwords in a row block the account.
		h.halt(fmt.Sprintf("couldn't sign in to Healdan as `%s`: %v",
			h.conf.Username, err))
		return false
	}

	properties, err := h.resolveProperties()
	if err != nil {
		if retryable(err) {
			log.Printf("WARN:  [%s] couldn't read folder %d's form from "+
				"Healdan, will try again: %v", h.inputName,
				h.conf.FolderID, err)
			return false
		}
		h.halt(fmt.Sprintf("can't file into Healdan folder %d: %v",
			h.conf.FolderID, err))
		return false
	}

	h.properties = properties
	h.ready = true
	return true
}

func (h *healdanOutputHandler) halt(reason string) {
	h.halted = true
	log.Printf("ERROR: [%s] %s", h.inputName, reason)
	log.Printf("ERROR: [%s] nothing more will be filed in Healdan until "+
		"this is fixed and the agent is restarted; print jobs are kept "+
		"in `%s` meanwhile", h.inputName, h.conf.SpoolDir)
}

// healdanConfigError is a mistake in our own configuration that Healdan's
// answers showed up.
type healdanConfigError struct {
	msg string
}

func (e *healdanConfigError) Error() string { return e.msg }

// resolveProperties turns the configured properties into what Healdan
// takes: a property that holds a user (Author, Typist) is configured as a
// username and sent as that user's id.
func (h *healdanOutputHandler) resolveProperties() (map[string]any, error) {
	fields, err := h.client.folderForm(h.conf.FolderID)
	if err != nil {
		return nil, err
	}
	types := make(map[string]string, len(fields))
	for _, f := range fields {
		types[f.Name] = f.Type
	}

	properties := make(map[string]any, len(h.conf.Properties))
	for name, value := range h.conf.Properties {
		fieldType, ok := types[name]
		if !ok {
			return nil, &healdanConfigError{fmt.Sprintf(
				"the folder has no property named `%s`", name)}
		}
		if fieldType != "user" {
			properties[name] = value
			continue
		}
		id, err := h.client.findUserID(value)
		if err != nil {
			var noUser *noSuchUserError
			if errors.As(err, &noUser) {
				return nil, &healdanConfigError{fmt.Sprintf(
					"property `%s`: %v", name, err)}
			}
			return nil, err
		}
		properties[name] = map[string]any{"id": id}
	}
	return properties, nil
}

func isStatus(err error, status int) bool {
	var he *healdanError
	return errors.As(err, &he) && he.Status == status
}

// retryable says whether a failed request is worth sending again as it
// is: Healdan couldn't be reached, is busy, or had an error of its own.
// Any other answer is a refusal that won't change by itself.
func retryable(err error) bool {
	var configErr *healdanConfigError
	if errors.As(err, &configErr) {
		return false
	}
	var he *healdanError
	if !errors.As(err, &he) {
		return true // never got an answer
	}
	return he.Status == 429 || he.Status >= 500
}
