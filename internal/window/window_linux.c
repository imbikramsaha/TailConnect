//go:build linux && cgo && webkit

#include <string.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

extern void tcOpenExternal(char *url);

static GtkWidget *win;
static WebKitWebView *view;

static void on_message(WebKitUserContentManager *m, WebKitJavascriptResult *r, gpointer data) {
  JSCValue *v = webkit_javascript_result_get_js_value(r);
  char *s = jsc_value_to_string(v);
  tcOpenExternal(s);
  g_free(s);
}

static char *app_origin; // "http://127.0.0.1:PORT/"

// The window only ever shows TailConnect's own page; other links (new
// windows or navigations) go to the system browser.
static gboolean on_decide(WebKitWebView *v, WebKitPolicyDecision *d, WebKitPolicyDecisionType type, gpointer data) {
  if (type != WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION && type != WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION)
    return FALSE;
  WebKitNavigationAction *a = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(d));
  const char *uri = webkit_uri_request_get_uri(webkit_navigation_action_get_request(a));
  if (type == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION && app_origin && uri &&
      g_str_has_prefix(uri, app_origin))
    return FALSE; // our own page: allow
  if (uri) tcOpenExternal((char *)uri);
  webkit_policy_decision_ignore(d);
  return TRUE;
}

static void set_origin(const char *url) {
  // Keep scheme://host:port/ of the first URL as the allowed origin.
  const char *p = strstr(url, "://");
  if (!p) return;
  const char *slash = strchr(p + 3, '/');
  size_t n = slash ? (size_t)(slash - url) + 1 : strlen(url);
  g_free(app_origin);
  app_origin = g_strndup(url, n);
}

void tcLinuxInit(const char *url) {
  set_origin(url);
  gtk_init(NULL, NULL);
  g_set_prgname("tailconnect");
  win = gtk_window_new(GTK_WINDOW_TOPLEVEL);
  gtk_window_set_title(GTK_WINDOW(win), "TailConnect");
  gtk_window_set_default_size(GTK_WINDOW(win), 1180, 800);
  /* Minimum size; keep in sync with MinWidth/MinHeight in window.go. */
  gtk_widget_set_size_request(win, 720, 540);
  gtk_window_set_icon_name(GTK_WINDOW(win), "tailconnect");
  gtk_window_set_position(GTK_WINDOW(win), GTK_WIN_POS_CENTER);

  WebKitUserContentManager *m = webkit_user_content_manager_new();
  webkit_user_content_manager_register_script_message_handler(m, "tc");
  g_signal_connect(m, "script-message-received::tc", G_CALLBACK(on_message), NULL);
  WebKitUserScript *s = webkit_user_script_new(
      "window.tcOpenExternal = function (u) { window.webkit.messageHandlers.tc.postMessage(String(u)); };",
      WEBKIT_USER_CONTENT_INJECT_TOP_FRAME, WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, NULL, NULL);
  webkit_user_content_manager_add_script(m, s);
  webkit_user_script_unref(s);

  view = WEBKIT_WEB_VIEW(webkit_web_view_new_with_user_content_manager(m));
  g_signal_connect(view, "decide-policy", G_CALLBACK(on_decide), NULL);
  gtk_container_add(GTK_CONTAINER(win), GTK_WIDGET(view));
  g_signal_connect(win, "destroy", G_CALLBACK(gtk_main_quit), NULL);
  webkit_web_view_load_uri(view, url);
  gtk_widget_show_all(win);
  gtk_window_present(GTK_WINDOW(win));
}

static gboolean nav_idle(gpointer p) {
  webkit_web_view_load_uri(view, (const char *)p);
  gtk_window_present(GTK_WINDOW(win));
  g_free(p);
  return G_SOURCE_REMOVE;
}

void tcLinuxNavigate(const char *url) { g_idle_add(nav_idle, g_strdup(url)); }

static gboolean quit_idle(gpointer p) {
  gtk_main_quit();
  return G_SOURCE_REMOVE;
}

void tcLinuxQuit(void) { g_idle_add(quit_idle, NULL); }

void tcLinuxRun(void) { gtk_main(); }
