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

// ---- notifications --------------------------------------------------------------------------------------------
//
// UNUserNotificationCenter. Notifications only work when the app runs from /Applications or ~/Applications and the
// bundle is signed (ad hoc is enough). Categories and requests come from Go as JSON (internal/menubar/notify.go owns
// what they contain); responses go back through goNotifyAction.

static NSString *str(const char *s) { return s ? [NSString stringWithUTF8String:s] : @""; }

static id parseJSON(const char *json) {
  NSData *d = [NSData dataWithBytes:json length:strlen(json)];
  return [NSJSONSerialization JSONObjectWithData:d options:0 error:nil];
}

@interface HALNotificationDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation HALNotificationDelegate
// Show notifications even while the app is "active" (an agent app often is, for a moment after a click).
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))handler {
  handler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList | UNNotificationPresentationOptionSound);
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
    didReceiveNotificationResponse:(UNNotificationResponse *)response
             withCompletionHandler:(void (^)(void))handler {
  NSString *text = @"";
  if ([response isKindOfClass:[UNTextInputNotificationResponse class]]) {
    text = ((UNTextInputNotificationResponse *)response).userText ?: @"";
  }
  UNNotificationRequest *req = response.notification.request;
  NSDictionary *info = req.content.userInfo ?: @{};
  NSData *infoData = [NSJSONSerialization dataWithJSONObject:info options:0 error:nil];
  NSString *infoJSON = infoData ? [[NSString alloc] initWithData:infoData encoding:NSUTF8StringEncoding] : @"{}";
  goNotifyAction((char *)response.actionIdentifier.UTF8String, (char *)text.UTF8String, (char *)req.identifier.UTF8String,
                 (char *)req.content.threadIdentifier.UTF8String, (char *)infoJSON.UTF8String);
  handler();
}
@end

static HALNotificationDelegate *gDelegate;

char *hal_notify_setup(const char *categoriesJSON) {
  __block char *err = NULL;
  void (^setup)(void) = ^{
    NSArray *defs = parseJSON(categoriesJSON);
    if (![defs isKindOfClass:[NSArray class]]) {
      err = copyString(@"categories are not a JSON array");
      return;
    }
    NSMutableSet *cats = [NSMutableSet set];
    for (NSDictionary *c in defs) {
      NSMutableArray *actions = [NSMutableArray array];
      for (NSDictionary *a in c[@"actions"]) {
        UNNotificationActionOptions opts = [a[@"destructive"] boolValue] ? UNNotificationActionOptionDestructive : UNNotificationActionOptionNone;
        if ([a[@"text"] boolValue]) {
          [actions addObject:[UNTextInputNotificationAction actionWithIdentifier:a[@"id"] title:a[@"title"] options:opts
                                                            textInputButtonTitle:a[@"sendTitle"] ?: @"Send"
                                                            textInputPlaceholder:a[@"placeholder"] ?: @""]];
        } else {
          [actions addObject:[UNNotificationAction actionWithIdentifier:a[@"id"] title:a[@"title"] options:opts]];
        }
      }
      [cats addObject:[UNNotificationCategory categoryWithIdentifier:c[@"id"] actions:actions intentIdentifiers:@[]
                                                             options:UNNotificationCategoryOptionNone]];
    }
    UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
    gDelegate = [HALNotificationDelegate new];
    center.delegate = gDelegate;
    [center setNotificationCategories:cats];
  };
  if ([NSThread isMainThread]) {
    setup();
  } else {
    dispatch_sync(dispatch_get_main_queue(), setup);
  }
  return err;
}

// Reads the notification settings the user chose. Blocks until the system answers; call it off the main thread.
hal_notify_settings hal_notify_settings_read(void) {
  __block hal_notify_settings r = {0, 0, 0, 0};
  dispatch_semaphore_t done = dispatch_semaphore_create(0);
  [[UNUserNotificationCenter currentNotificationCenter] getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *s) {
    r.auth = (long)s.authorizationStatus;
    r.alertStyle = (long)s.alertStyle;
    r.alertSetting = (long)s.alertSetting;
    r.centerSetting = (long)s.notificationCenterSetting;
    dispatch_semaphore_signal(done);
  }];
  dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
  return r;
}

// Asks for permission; the system shows its prompt once, the first time. The answer arrives in goNotifyAuth.
void hal_notify_request_auth(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    [NSApp activateIgnoringOtherApps:YES]; // the prompt belongs to the frontmost app
    [[UNUserNotificationCenter currentNotificationCenter]
        requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                      completionHandler:^(BOOL granted, NSError *error) {
                        char *msg = error ? copyString(errString(error)) : NULL;
                        goNotifyAuth(granted ? 1 : 0, msg);
                        free(msg);
                      }];
  });
}

// Posts a notification described by JSON (see menubar.Request). Returns an error for a request that cannot be made;
// delivery failures are logged through goLog.
char *hal_notify_post(const char *json) {
  NSDictionary *r = parseJSON(json);
  if (![r isKindOfClass:[NSDictionary class]]) return copyString(@"notification is not a JSON object");
  UNMutableNotificationContent *content = [UNMutableNotificationContent new];
  content.title = r[@"title"] ?: @"";
  content.subtitle = r[@"subtitle"] ?: @"";
  content.body = r[@"body"] ?: @"";
  content.threadIdentifier = r[@"thread"] ?: @"";
  if ([r[@"category"] length] > 0) content.categoryIdentifier = r[@"category"];
  if ([r[@"userInfo"] isKindOfClass:[NSDictionary class]]) content.userInfo = r[@"userInfo"];
  if ([r[@"sound"] boolValue]) content.sound = [UNNotificationSound defaultSound];
  // No interruption level: time-sensitive needs an entitlement an ad-hoc signed app cannot have.
  NSString *rid = r[@"id"] ?: [[NSUUID UUID] UUIDString];
  UNNotificationRequest *req = [UNNotificationRequest requestWithIdentifier:rid content:content trigger:nil];
  [[UNUserNotificationCenter currentNotificationCenter] addNotificationRequest:req
                                                         withCompletionHandler:^(NSError *error) {
                                                           if (error) {
                                                             NSString *m = [NSString stringWithFormat:@"posting notification %@ failed: %@", rid, errString(error)];
                                                             goLog((char *)m.UTF8String);
                                                           }
                                                         }];
  return NULL;
}

void hal_notify_remove(const char *id) {
  NSArray *ids = @[ str(id) ];
  [[UNUserNotificationCenter currentNotificationCenter] removeDeliveredNotificationsWithIdentifiers:ids];
  [[UNUserNotificationCenter currentNotificationCenter] removePendingNotificationRequestsWithIdentifiers:ids];
}
