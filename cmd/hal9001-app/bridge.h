#ifndef HAL_BRIDGE_H
#define HAL_BRIDGE_H

// Objective-C side of the app (bridge.m), called from Go (bridge.go). Every function that returns a char * returns
// malloc'd memory the caller frees, or NULL.

// ---- app bundle ----
// CFBundleIdentifier of the running app; NULL when the binary does not run from an app bundle.
char *hal_bundle_identifier(void);
// A string (or number) value of the main bundle's Info.plist, as a string; NULL when the key is absent.
char *hal_info_string(const char *key);

// ---- SMAppService ----
// kind 0 is the LaunchAgent named by plist (a file in Contents/Library/LaunchAgents of the bundle), kind 1 the app
// itself as a login item. status is the SMAppServiceStatus after the call: 0 not registered, 1 enabled,
// 2 requires approval, 3 not found. error describes a failed register/unregister, NULL otherwise.
typedef struct {
  long status;
  char *error;
} hal_sm_result;

hal_sm_result hal_sm_status(int kind, const char *plist);
hal_sm_result hal_sm_register(int kind, const char *plist);
hal_sm_result hal_sm_unregister(int kind, const char *plist);
// Opens System Settings > General > Login Items.
void hal_open_login_items(void);

// ---- alerts ----
// Shows a modal alert on the main thread and returns 1 when the first (ok) button was pressed. cancel may be empty
// for an alert with a single button. Safe to call from any thread.
int hal_alert(const char *title, const char *message, const char *ok, const char *cancel);

// ---- notifications (UNUserNotificationCenter) ----
// Implemented in Go (bridge.go, //export). goNotifyAction reports a click or button press with the userInfo the
// notification was posted with, as JSON. goNotifyAuth reports the answer of the authorization prompt.
void goLog(char *msg);
void goNotifyAction(char *action, char *text, char *requestID, char *threadID, char *userInfoJSON);
void goNotifyAuth(int granted, char *error);

// The user's choices, from getNotificationSettings: UNAuthorizationStatus, UNAlertStyle, and UNNotificationSetting
// (0 not supported, 1 disabled, 2 enabled) of the alert and notification center switches.
typedef struct {
  long auth;
  long alertStyle;
  long alertSetting;
  long centerSetting;
} hal_notify_settings;

// Installs the delegate and registers the categories (a JSON array of menubar.CategoryDef). Returns an error or NULL.
char *hal_notify_setup(const char *categoriesJSON);
// Blocks until the system answers; not for the main thread.
hal_notify_settings hal_notify_settings_read(void);
// Shows the system's authorization prompt (only the first time); the answer arrives in goNotifyAuth.
void hal_notify_request_auth(void);
// Posts a notification from JSON (menubar.Request). Returns an error or NULL; delivery errors go to goLog.
char *hal_notify_post(const char *json);
void hal_notify_remove(const char *id);

#endif
