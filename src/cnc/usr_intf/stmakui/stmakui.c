/*
 * stmakui — Generic WebKit container for LinuxCNC web UIs.
 *
 * When invoked directly:
 *   stmakui --url URL [--title TITLE] [--width W] [--height H]
 *   stmakui --path PATH [--instance NAME] ...
 *
 * --path names a page on the server (e.g. /app/halshow/); the base URL and
 * the ?instance= value are added the same way the profiles below do it.
 *
 * With --xid XID the view is not a top-level window but a GtkPlug embedded
 * (XEmbed) into the foreign X window XID. AXIS uses this for webapp tabs and
 * its webapp side panel. Embedded mode also forwards the keys the page does
 * not handle to $AXIS_FORWARD_EVENTS_TO, and keeps retrying while the server
 * is unreachable, since there is no user-visible window to reload by hand.
 *
 * When invoked via a symlink (e.g. "halscope"):
 *   The basename of argv[0] selects built-in defaults for URL path,
 *   window title, and size.  The server base URL comes from the
 *   STMAK_REST_URL environment variable (default: http://127.0.0.1:5080).
 *
 * Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
 * License: GPLv2
 */

#include <gtk/gtk.h>
#include <gtk/gtkx.h>
#include <gdk/gdkx.h>
#include <X11/Xlib.h>
#include <webkit2/webkit2.h>
#include <libgen.h>
#include <string.h>
#include <stdio.h>
#include <stdlib.h>

#define DEFAULT_REST_URL "http://127.0.0.1:5080"
#define ENV_REST_URL     "STMAK_REST_URL"
#define ENV_INSTANCE     "STMAK_TASK_INSTANCE"
#define DEFAULT_INSTANCE "milltask"
#define ENV_FORWARD_KEYS "AXIS_FORWARD_EVENTS_TO"

#define RETRY_MIN_MS     1000
#define RETRY_MAX_MS     10000

/* Built-in app profiles, selected by symlink name. */
typedef struct {
    const char *name;       /* argv[0] basename to match */
    const char *path;       /* URL path appended to base URL */
    const char *title;      /* window title */
    int         width;
    int         height;
    const char *instance;   /* default ?instance= value (STMAK_TASK_INSTANCE overrides) */
} app_profile_t;

static const app_profile_t profiles[] = {
    { "halscope",  "/app/halscope/",  "HAL Oscilloscope", 1280, 800, DEFAULT_INSTANCE },
    { "halshow",   "/app/halshow/",   "HAL Show",         1024, 700, DEFAULT_INSTANCE },
    { "emccalib",  "/app/emccalib/",  "EMC Calibration",   900, 700, DEFAULT_INSTANCE },
    { "tooledit",  "/app/tooledit/",  "Tool Editor",       900, 700, DEFAULT_INSTANCE },
    { "latency",   "/app/latency/",   "RT Latency",       1100, 760, DEFAULT_INSTANCE },
    /* The only profile whose name is not the app directory: bare "linuxcnctop"
     * is the python launcher (window by default, text with -t), so the viewer
     * it execs into carries the -ui suffix. The app, and its URL, keep the
     * plain name. */
    { "linuxcnctop-ui", "/app/linuxcnctop/", "LinuxCNC Status", 900, 800, DEFAULT_INSTANCE },
    /* The classicladder app addresses the instance named "classicladder",
     * not the milltask default. */
    { "classicladder", "/app/classicladder/", "Classic Ladder", 1200, 900, "classicladder" },
    { NULL, NULL, NULL, 0, 0, NULL }
};

static const app_profile_t *find_profile(const char *name)
{
    for (const app_profile_t *p = profiles; p->name; p++) {
        if (strcmp(p->name, name) == 0)
            return p;
    }
    return NULL;
}

static const char *get_base_url(void)
{
    const char *env = getenv(ENV_REST_URL);
    return (env && *env) ? env : DEFAULT_REST_URL;
}

static void on_destroy(GtkWidget *widget, gpointer data)
{
    (void)widget;
    (void)data;
    gtk_main_quit();
}

/* ------------------------------------------------------------------------
 * Embedded mode: key forwarding
 *
 * Inside AXIS the plug receives every key while it has the focus, including
 * the jog and abort keys AXIS binds on its own window. WebKit handles a key
 * asynchronously: the view first claims it, asks the web process, and if the
 * page did not consume it re-dispatches the same event, which then bubbles up
 * to the plug. A key-press handler connected *after* the default one therefore
 * sees exactly the keys the page left alone, and sends those on to AXIS as
 * synthetic X events (the same thing gladevcp's xembed.keyboard_forward does).
 *
 * Releases need care, because AXIS jogs from press to release: a release must
 * reach AXIS for every press that did, or the axis keeps moving. The release
 * of a quick tap can arrive before the web process has answered for the
 * press, so a release with no forwarded press is remembered and sent right
 * after the press if that press turns out to be forwarded later.
 * ------------------------------------------------------------------------ */

static Display *fwd_display;
static Window   fwd_window;
static gboolean fwd_down[256];          /* press forwarded, release pending */
static guint32  early_release[256];     /* release seen before the press was forwarded */

static void forward_key(int type, guint keycode, guint state, guint32 time)
{
    XKeyEvent xe = { 0 };
    xe.type = type;
    xe.display = fwd_display;
    xe.window = fwd_window;
    xe.root = DefaultRootWindow(fwd_display);
    xe.subwindow = None;
    xe.time = time;
    xe.state = state & 0xff;
    xe.keycode = keycode;
    xe.same_screen = True;
    XSendEvent(fwd_display, fwd_window, False, 0, (XEvent *)&xe);
    XFlush(fwd_display);
}

static gboolean on_key_press_unhandled(GtkWidget *widget, GdkEventKey *ev, gpointer data)
{
    (void)widget;
    (void)data;
    guint kc = ev->hardware_keycode & 0xff;
    guint32 rel = early_release[kc];

    early_release[kc] = 0;
    forward_key(KeyPress, kc, ev->state, ev->time);
    if (rel && (gint32)(rel - ev->time) >= 0)
        forward_key(KeyRelease, kc, ev->state, rel);
    else
        fwd_down[kc] = TRUE;
    return TRUE;
}

static gboolean on_key_release(GtkWidget *widget, GdkEventKey *ev, gpointer data)
{
    (void)widget;
    (void)data;
    guint kc = ev->hardware_keycode & 0xff;

    if (fwd_down[kc]) {
        fwd_down[kc] = FALSE;
        forward_key(KeyRelease, kc, ev->state, ev->time);
    } else {
        early_release[kc] = ev->time;
    }
    return FALSE;   /* the page sees its releases too */
}

/* Losing the focus while a forwarded key is held would leave AXIS with a
 * press and no release; send the releases now. */
static gboolean on_focus_out(GtkWidget *widget, GdkEventFocus *ev, gpointer data)
{
    (void)widget;
    (void)ev;
    (void)data;
    for (guint kc = 0; kc < 256; kc++) {
        if (fwd_down[kc]) {
            fwd_down[kc] = FALSE;
            forward_key(KeyRelease, kc, 0, CurrentTime);
        }
    }
    return FALSE;
}

/* Tk never passes the X input focus on to a foreign embedded window: it stays
 * on Tk's own focus window, and the plug would never see a key. So the plug
 * takes the focus itself when the page is clicked, as a top-level window
 * would get it from the window manager; AXIS takes it back on a click into
 * any of its own widgets. */
static gboolean on_button_press_take_focus(GtkWidget *widget, GdkEventButton *ev, gpointer data)
{
    GtkWidget *plug = data;
    GdkWindow *win = gtk_widget_get_window(plug);
    (void)widget;
    if (win) {
        XSetInputFocus(GDK_WINDOW_XDISPLAY(win), GDK_WINDOW_XID(win), RevertToParent, ev->time);
        XFlush(GDK_WINDOW_XDISPLAY(win));
    }
    return FALSE;
}

static void setup_key_forwarding(GtkWidget *plug)
{
    const char *env = getenv(ENV_FORWARD_KEYS);
    if (!env || !*env)
        return;
    char *end;
    unsigned long xid = strtoul(env, &end, 0);
    if (*end || !xid) {
        fprintf(stderr, "stmakui: ignoring invalid %s=%s\n", ENV_FORWARD_KEYS, env);
        return;
    }
    fwd_display = GDK_DISPLAY_XDISPLAY(gtk_widget_get_display(plug));
    fwd_window = (Window)xid;
    g_signal_connect_after(plug, "key-press-event", G_CALLBACK(on_key_press_unhandled), NULL);
    g_signal_connect(plug, "key-release-event", G_CALLBACK(on_key_release), NULL);
    g_signal_connect(plug, "focus-out-event", G_CALLBACK(on_focus_out), NULL);
}

/* ------------------------------------------------------------------------
 * Embedded mode: retry while the server is unreachable
 *
 * AXIS may come up before stmakd serves the page, and stmakd may restart
 * under a running AXIS. A failed load shows a short placeholder and retries
 * with a backoff; a crashed web process is simply reloaded.
 * ------------------------------------------------------------------------ */

static const char *target_url;
static guint retry_ms = RETRY_MIN_MS;
static guint retry_source;
static gboolean placeholder_loading;

static gboolean retry_load(gpointer data)
{
    WebKitWebView *webview = data;
    retry_source = 0;
    webkit_web_view_load_uri(webview, target_url);
    return G_SOURCE_REMOVE;
}

static void schedule_retry(WebKitWebView *webview)
{
    if (retry_source)
        return;
    retry_source = g_timeout_add(retry_ms, retry_load, webview);
    retry_ms = MIN(retry_ms * 2, RETRY_MAX_MS);
}

static gboolean on_load_failed(WebKitWebView *webview, WebKitLoadEvent load_event,
                               gchar *failing_uri, GError *error, gpointer data)
{
    (void)load_event;
    (void)data;
    /* A load replaced by another one (a click, a redirect) is not a failure. */
    if (g_error_matches(error, WEBKIT_NETWORK_ERROR, WEBKIT_NETWORK_ERROR_CANCELLED) ||
        g_error_matches(error, WEBKIT_POLICY_ERROR,
                        WEBKIT_POLICY_ERROR_FRAME_LOAD_INTERRUPTED_BY_POLICY_CHANGE))
        return FALSE;

    char *msg = g_markup_printf_escaped(
        "<html><body style=\"font-family:sans-serif;color:#666;padding:1em\">"
        "<p>Waiting for %s &hellip;</p><p><small>%s</small></p></body></html>",
        failing_uri, error->message);
    placeholder_loading = TRUE;
    webkit_web_view_load_alternate_html(webview, msg, failing_uri, NULL);
    g_free(msg);
    schedule_retry(webview);
    return TRUE;
}

static void on_load_changed(WebKitWebView *webview, WebKitLoadEvent load_event, gpointer data)
{
    (void)webview;
    (void)data;
    if (load_event != WEBKIT_LOAD_COMMITTED)
        return;
    /* A failed load never commits, so a commit is either our placeholder or
     * the page itself; only the page resets the backoff. */
    if (placeholder_loading)
        placeholder_loading = FALSE;
    else
        retry_ms = RETRY_MIN_MS;
}

static void on_web_process_terminated(WebKitWebView *webview,
                                      WebKitWebProcessTerminationReason reason, gpointer data)
{
    (void)reason;
    (void)data;
    fprintf(stderr, "stmakui: web process terminated, reloading %s\n", target_url);
    schedule_retry(webview);
}

int main(int argc, char *argv[])
{
    const char *url = NULL;
    const char *path = NULL;
    const char *instance = NULL;
    const char *title = NULL;
    unsigned long xid = 0;
    int width = 1280;
    int height = 800;

    /* Determine invocation name (strip path). */
    char *progname = basename(argv[0]);
    const app_profile_t *profile = find_profile(progname);

    /* Parse command-line arguments. */
    for (int i = 1; i < argc; i++) {
        if ((strcmp(argv[i], "--url") == 0 || strcmp(argv[i], "-u") == 0) && i + 1 < argc) {
            url = argv[++i];
        } else if ((strcmp(argv[i], "--path") == 0 || strcmp(argv[i], "-p") == 0) && i + 1 < argc) {
            path = argv[++i];
        } else if ((strcmp(argv[i], "--instance") == 0 || strcmp(argv[i], "-i") == 0) && i + 1 < argc) {
            instance = argv[++i];
        } else if ((strcmp(argv[i], "--xid") == 0 || strcmp(argv[i], "-x") == 0) && i + 1 < argc) {
            char *end;
            xid = strtoul(argv[++i], &end, 0);
            if (*end || !xid) {
                fprintf(stderr, "%s: invalid window id: %s\n", progname, argv[i]);
                return 1;
            }
        } else if ((strcmp(argv[i], "--title") == 0 || strcmp(argv[i], "-t") == 0) && i + 1 < argc) {
            title = argv[++i];
        } else if ((strcmp(argv[i], "--width") == 0 || strcmp(argv[i], "-W") == 0) && i + 1 < argc) {
            width = atoi(argv[++i]);
        } else if ((strcmp(argv[i], "--height") == 0 || strcmp(argv[i], "-H") == 0) && i + 1 < argc) {
            height = atoi(argv[++i]);
        } else if (strcmp(argv[i], "--help") == 0 || strcmp(argv[i], "-h") == 0) {
            printf("Usage: %s [--url URL | --path PATH] [--instance NAME] [--xid XID]\n"
                   "       %*s [--title TITLE] [--width W] [--height H]\n\n",
                   progname, (int)strlen(progname), "");
            printf("--path   page on the server, e.g. /app/halshow/ (base URL and ?instance= added)\n");
            printf("--xid    embed into X window XID (XEmbed) instead of opening a window\n\n");
            printf("When invoked via symlink (e.g. halscope), built-in defaults are used.\n");
            printf("Server URL from %s env var (default: %s)\n", ENV_REST_URL, DEFAULT_REST_URL);
            printf("\nKnown app profiles:\n");
            for (const app_profile_t *p = profiles; p->name; p++)
                printf("  %-12s %s (%dx%d)\n", p->name, p->title, p->width, p->height);
            return 0;
        } else if (argv[i][0] == '-') {
            fprintf(stderr, "%s: unknown argument: %s\n", progname, argv[i]);
            return 1;
        }
        /* Ignore positional args (e.g. file path passed by axis). */
    }

    /* Build URL from profile if not given explicitly. */
    char url_buf[1024];
    if (url && path) {
        fprintf(stderr, "%s: --url and --path are mutually exclusive\n", progname);
        return 1;
    }
    if (!url) {
        if (!path && !profile) {
            fprintf(stderr, "%s: no --url or --path given and no built-in profile for '%s'\n",
                    progname, progname);
            fprintf(stderr, "Usage: %s --url URL|--path PATH [--title TITLE] [--width W] [--height H]\n", progname);
            return 1;
        }
        const char *base = get_base_url();
        const char *inst = instance;
        if (!inst || !*inst)
            inst = getenv(ENV_INSTANCE);
        if (!inst || !*inst)
            inst = (profile && !path) ? profile->instance : DEFAULT_INSTANCE;
        if (!path)
            path = profile->path;
        /* A trailing slash on the base or a missing leading one on the path
         * must not change the URL. */
        size_t blen = strlen(base);
        while (blen > 0 && base[blen - 1] == '/')
            blen--;
        while (*path == '/')
            path++;
        int n = snprintf(url_buf, sizeof(url_buf), "%.*s/%s%sinstance=%s",
                         (int)blen, base, path, strchr(path, '?') ? "&" : "?", inst);
        if (n < 0 || (size_t)n >= sizeof(url_buf)) {
            fprintf(stderr, "%s: URL too long\n", progname);
            return 1;
        }
        url = url_buf;
    }

    if (!title)
        title = profile ? profile->title : "LinuxCNC";
    if (profile) {
        /* Only use profile size if not overridden on command line. */
        if (width == 1280 && profile->width != 1280)
            width = profile->width;
        if (height == 800 && profile->height != 800)
            height = profile->height;
    }

    g_set_prgname(progname);
    /* XEmbed exists only on X11; under Wayland this runs through XWayland,
     * like the Tk window it embeds into. */
    if (xid)
        gdk_set_allowed_backends("x11");
    gtk_init(&argc, &argv);

    GtkWidget *window;
    if (xid) {
        window = gtk_plug_new((Window)xid);
    } else {
        window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
        gtk_window_set_title(GTK_WINDOW(window), title);
        gtk_window_set_default_size(GTK_WINDOW(window), width, height);
    }
    /* A plug is destroyed with its socket, so AXIS closing ends us too. */
    g_signal_connect(window, "destroy", G_CALLBACK(on_destroy), NULL);

    WebKitWebView *webview = WEBKIT_WEB_VIEW(webkit_web_view_new());

    /* Enable developer tools for debugging. */
    WebKitSettings *settings = webkit_web_view_get_settings(webview);
    webkit_settings_set_enable_developer_extras(settings, TRUE);

    if (xid) {
        target_url = url;
        g_signal_connect(webview, "load-failed", G_CALLBACK(on_load_failed), NULL);
        g_signal_connect(webview, "load-changed", G_CALLBACK(on_load_changed), NULL);
        g_signal_connect(webview, "web-process-terminated",
                         G_CALLBACK(on_web_process_terminated), NULL);
        setup_key_forwarding(window);
        g_signal_connect(webview, "button-press-event",
                         G_CALLBACK(on_button_press_take_focus), window);
    }

    webkit_web_view_load_uri(webview, url);

    gtk_container_add(GTK_CONTAINER(window), GTK_WIDGET(webview));
    gtk_widget_show_all(window);

    /* XEmbed leaves mapping the plug to the embedder, and Tk does not speak
     * XEmbed, so map it ourselves (gladevcp's xembed.reparent does the same).
     * Tk owns the container's geometry and sizes the plug to it. */
    if (xid) {
        GdkWindow *win = gtk_widget_get_window(window);
        XMapWindow(GDK_WINDOW_XDISPLAY(win), GDK_WINDOW_XID(win));
        XFlush(GDK_WINDOW_XDISPLAY(win));
    }

    gtk_main();

    return 0;
}
