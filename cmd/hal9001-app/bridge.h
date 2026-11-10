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

#endif
