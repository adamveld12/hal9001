#import <Cocoa/Cocoa.h>
#import <Foundation/Foundation.h>
#import <ServiceManagement/ServiceManagement.h>
#import <UserNotifications/UserNotifications.h>
#include <stdlib.h>
#include <string.h>

#include "bridge.h"

// fyne.io/systray owns the main thread and NSApplication; everything here runs on that run loop (or is thread-safe).

static char *copyString(NSString *s) { return s ? strdup(s.UTF8String) : NULL; }

static NSString *errString(NSError *e) {
  return [NSString stringWithFormat:@"%@ (domain %@, code %ld)", e.localizedDescription, e.domain, (long)e.code];
}

// ---- app bundle -----------------------------------------------------------------------------------------------

char *hal_bundle_identifier(void) { return copyString([NSBundle mainBundle].bundleIdentifier); }

char *hal_info_string(const char *key) {
  id v = [[NSBundle mainBundle] objectForInfoDictionaryKey:[NSString stringWithUTF8String:key]];
  if ([v isKindOfClass:[NSString class]]) return copyString(v);
  if ([v isKindOfClass:[NSNumber class]]) return copyString([v stringValue]);
  return NULL;
}

// ---- SMAppService ---------------------------------------------------------------------------------------------

static SMAppService *serviceFor(int kind, const char *plist) {
  if (kind == 0) return [SMAppService agentServiceWithPlistName:[NSString stringWithUTF8String:plist]];
  return [SMAppService mainAppService];
}

hal_sm_result hal_sm_status(int kind, const char *plist) {
  hal_sm_result r = {(long)serviceFor(kind, plist).status, NULL};
  return r;
}

hal_sm_result hal_sm_register(int kind, const char *plist) {
  SMAppService *s = serviceFor(kind, plist);
  NSError *err = nil;
  [s registerAndReturnError:&err];
  hal_sm_result r = {(long)s.status, err ? copyString(errString(err)) : NULL};
  return r;
}

hal_sm_result hal_sm_unregister(int kind, const char *plist) {
  SMAppService *s = serviceFor(kind, plist);
  NSError *err = nil;
  [s unregisterAndReturnError:&err];
  hal_sm_result r = {(long)s.status, err ? copyString(errString(err)) : NULL};
  return r;
}

void hal_open_login_items(void) { [SMAppService openSystemSettingsLoginItems]; }

// ---- alerts ---------------------------------------------------------------------------------------------------

int hal_alert(const char *title, const char *message, const char *ok, const char *cancel) {
  NSString *t = [NSString stringWithUTF8String:title];
  NSString *m = [NSString stringWithUTF8String:message];
  NSString *o = [NSString stringWithUTF8String:ok];
  NSString *c = [NSString stringWithUTF8String:cancel];
  __block int pressedOK = 0;
  void (^show)(void) = ^{
    [NSApp activateIgnoringOtherApps:YES];
    NSAlert *a = [[NSAlert alloc] init];
    a.messageText = t;
    a.informativeText = m;
    [a addButtonWithTitle:o];
    if (c.length > 0) [a addButtonWithTitle:c];
    pressedOK = [a runModal] == NSAlertFirstButtonReturn;
  };
  if ([NSThread isMainThread]) {
    show();
  } else {
    dispatch_sync(dispatch_get_main_queue(), show);
  }
  return pressedOK;
}

// ---- notifications (next step) --------------------------------------------------------------------------------
//
// Native notifications are added here, not in Go: a UNUserNotificationCenterDelegate on the main run loop
// (willPresent: banner|list|sound; didReceive: forwards the action id, typed text, request id and thread id to a Go
// callback exported from bridge.go), category registration, and post/remove functions behind the menubar.Notifier
// interface (internal/menubar/notify.go). UserNotifications is already linked (see the #cgo lines in bridge.go);
// notifications only work when the app runs from /Applications or ~/Applications.
