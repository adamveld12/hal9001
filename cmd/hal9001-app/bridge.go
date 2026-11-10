//go:build darwin

package main

/*
#cgo darwin CFLAGS: -fobjc-arc -mmacosx-version-min=13.0
#cgo darwin LDFLAGS: -framework Cocoa -framework Foundation -framework UserNotifications -framework ServiceManagement -mmacosx-version-min=13.0
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"unsafe"

	"hal9001/internal/menubar"
)

// goString converts a malloc'd C string to a Go string and frees it; NULL gives "".
func goString(p *C.char) string {
	if p == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

func cString(s string) (*C.char, func()) {
	p := C.CString(s)
	return p, func() { C.free(unsafe.Pointer(p)) }
}

// bundleIdentifier is the app's CFBundleIdentifier; empty when the binary does not run from an app bundle.
func bundleIdentifier() string { return goString(C.hal_bundle_identifier()) }

// infoString is a value of the app's Info.plist as a string; empty when absent.
func infoString(key string) string {
	k, free := cString(key)
	defer free()
	return goString(C.hal_info_string(k))
}

// smKind selects what an SMAppService call acts on.
type smKind int

const (
	smAgent   smKind = 0 // the bundled LaunchAgent named by the plist argument
	smMainApp smKind = 1 // the app itself as a login item
)

// smStatus is SMAppServiceStatus.
type smStatus int

const (
	smNotRegistered    smStatus = 0
	smEnabled          smStatus = 1
	smRequiresApproval smStatus = 2
	smNotFound         smStatus = 3
)

func (s smStatus) String() string {
	switch s {
	case smNotRegistered:
		return "not registered"
	case smEnabled:
		return "enabled"
	case smRequiresApproval:
		return "requires approval"
	case smNotFound:
		return "not found"
	}
	return "unknown"
}

// smResult converts a result, freeing its error string.
func smResult(r C.hal_sm_result) (smStatus, error) {
	st := smStatus(r.status)
	if msg := goString(r.error); msg != "" {
		return st, errors.New(msg)
	}
	return st, nil
}

func smCall(kind smKind, plist string, f func(C.int, *C.char) C.hal_sm_result) (smStatus, error) {
	p, free := cString(plist)
	defer free()
	return smResult(f(C.int(kind), p))
}

// smStatusOf reads the current status of a service.
func smStatusOf(kind smKind, plist string) smStatus {
	st, _ := smCall(kind, plist, func(k C.int, p *C.char) C.hal_sm_result { return C.hal_sm_status(k, p) })
	return st
}

// smRegister registers a service with launchd / Login Items and returns its status afterwards.
func smRegister(kind smKind, plist string) (smStatus, error) {
	return smCall(kind, plist, func(k C.int, p *C.char) C.hal_sm_result { return C.hal_sm_register(k, p) })
}

// smUnregister removes a service; a bundled agent is stopped too.
func smUnregister(kind smKind, plist string) (smStatus, error) {
	return smCall(kind, plist, func(k C.int, p *C.char) C.hal_sm_result { return C.hal_sm_unregister(k, p) })
}

// openLoginItems opens System Settings > General > Login Items.
func openLoginItems() { C.hal_open_login_items() }

// alert shows a modal alert and reports whether the ok button was pressed. cancel may be empty for a single button.
func alert(title, message, ok, cancel string) bool {
	t, ft := cString(title)
	defer ft()
	m, fm := cString(message)
	defer fm()
	o, fo := cString(ok)
	defer fo()
	c, fc := cString(cancel)
	defer fc()
	return C.hal_alert(t, m, o, c) != 0
}

// ---- notifications ----

var (
	notifyMu       sync.Mutex
	notifyOnAction func(menubar.NotificationAction)
	notifyOnAuth   func(granted bool, errMsg string)
)

// setNotifyHandlers registers where responses to notifications and the answer of the authorization prompt go.
func setNotifyHandlers(onAction func(menubar.NotificationAction), onAuth func(granted bool, errMsg string)) {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	notifyOnAction, notifyOnAuth = onAction, onAuth
}

//export goLog
func goLog(msg *C.char) { slog.Info("objc", "msg", C.GoString(msg)) }

//export goNotifyAction
func goNotifyAction(action, text, requestID, threadID, userInfo *C.char) {
	a := menubar.NotificationAction{
		Action: C.GoString(action), Text: C.GoString(text), RequestID: C.GoString(requestID), ThreadID: C.GoString(threadID),
		UserInfo: map[string]string{},
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(C.GoString(userInfo)), &raw); err == nil {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				a.UserInfo[k] = s
			}
		}
	}
	notifyMu.Lock()
	h := notifyOnAction
	notifyMu.Unlock()
	if h != nil {
		go h(a) // this runs on the main thread: never block it on the network
	}
}

//export goNotifyAuth
func goNotifyAuth(granted C.int, errMsg *C.char) {
	notifyMu.Lock()
	h := notifyOnAuth
	notifyMu.Unlock()
	if h != nil {
		go h(granted != 0, C.GoString(errMsg))
	}
}

// notifySetup installs the notification delegate and registers the categories.
func notifySetup(categories []menubar.CategoryDef) error {
	data, err := json.Marshal(categories)
	if err != nil {
		return err
	}
	p, free := cString(string(data))
	defer free()
	if msg := goString(C.hal_notify_setup(p)); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// notifySettings reads what the user decided about this app's notifications.
func notifySettings() menubar.NotifySettings {
	r := C.hal_notify_settings_read()
	return menubar.NotifySettings{
		Auth:          menubar.AuthStatus(r.auth),
		Style:         menubar.AlertStyle(r.alertStyle),
		AlertEnabled:  r.alertSetting == 2,
		CenterEnabled: r.centerSetting == 2,
	}
}

// notifyRequestAuth shows the system's authorization prompt; the answer arrives through the handler set by
// setNotifyHandlers.
func notifyRequestAuth() { C.hal_notify_request_auth() }

func notifyPost(r menubar.Request) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	p, free := cString(string(data))
	defer free()
	if msg := goString(C.hal_notify_post(p)); msg != "" {
		return errors.New(msg)
	}
	return nil
}

func notifyRemove(ids ...string) {
	for _, id := range ids {
		p, free := cString(id)
		C.hal_notify_remove(p)
		free()
	}
}
