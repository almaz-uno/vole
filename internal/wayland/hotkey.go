package wayland

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/almaz-uno/vole/internal/platform"
	"github.com/godbus/dbus/v5"
)

// The GlobalShortcuts portal (org.freedesktop.portal.GlobalShortcuts, KWin
// 5.27+) registers shortcut *ids*; the user binds the actual keys in the
// compositor's settings. It emits Activated/Deactivated, mapping naturally to
// PTT press/release. We register two shortcuts so each language gets its own
// key — the portal cannot surface a live Shift state mid-press, so onLang is
// never emitted on Wayland.
const (
	portalBus    = "org.freedesktop.portal.Desktop"
	portalPath   = "/org/freedesktop/portal/desktop"
	gsIface      = "org.freedesktop.portal.GlobalShortcuts"
	sessionIface = "org.freedesktop.portal.Session"
	requestIface = "org.freedesktop.portal.Request"

	idDictate    = "dictate"     // base language
	idDictateAlt = "dictate-alt" // alternate (Shift) language

	// createTimeout bounds the non-interactive CreateSession handshake.
	createTimeout = 30 * time.Second
	// bindTimeout is 0 (no deadline): BindShortcuts is interactive on KDE — the
	// compositor shows a consent dialog and only answers once the user acts — so
	// we wait indefinitely (until Close) rather than abandoning the request.
	bindTimeout = 0
)

// HotkeyConfig describes the two PTT shortcuts to register.
type HotkeyConfig struct {
	LangBase  string // language for the "dictate" shortcut
	LangShift string // language for the "dictate-alt" shortcut (empty: single shortcut)
	// Mods/Key seed the suggested trigger sent to the portal: "dictate" gets
	// Mods+Key and "dictate-alt" gets Mods+Shift+Key. The compositor may pre-fill
	// these in its bind UI; the user can still override them. Empty: no suggestion.
	Mods string // e.g. "Super"
	Key  string // e.g. "k"
}

// Hotkey is a push-to-talk source backed by the GlobalShortcuts portal.
type Hotkey struct {
	conn    *dbus.Conn
	signals chan *dbus.Signal
	session dbus.ObjectPath
	cfg     HotkeyConfig
	quit    chan struct{}
}

var _ platform.Hotkey = (*Hotkey)(nil)

// portalShortcut marshals as the portal's (sa{sv}) shortcut tuple.
type portalShortcut struct {
	ID    string
	Props map[string]dbus.Variant
}

var tokenSeq atomic.Uint64

// NewHotkey opens a GlobalShortcuts session and binds the dictation shortcuts.
// It returns an error (so the daemon can run tray-only) if the portal is
// missing or unresponsive.
func NewHotkey(cfg HotkeyConfig) (*Hotkey, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("wayland hotkey: session bus: %w", err)
	}
	h := &Hotkey{
		conn:    conn,
		signals: make(chan *dbus.Signal, 32),
		cfg:     cfg,
		quit:    make(chan struct{}),
	}
	conn.Signal(h.signals)

	// Subscribe to shortcut activations before binding so none are missed.
	for _, member := range []string{"Activated", "Deactivated"} {
		if err := conn.AddMatchSignal(
			dbus.WithMatchInterface(gsIface), dbus.WithMatchMember(member),
		); err != nil {
			h.Close()
			return nil, fmt.Errorf("wayland hotkey: match %s: %w", member, err)
		}
	}

	obj := conn.Object(portalBus, portalPath)

	// 1. CreateSession — the session handle arrives via the request Response.
	res, err := h.request(obj, gsIface+".CreateSession", createTimeout, func(opts map[string]dbus.Variant) {
		opts["session_handle_token"] = dbus.MakeVariant(newToken("session"))
	})
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("wayland hotkey: CreateSession: %w", err)
	}
	h.session = sessionHandle(res)
	if h.session == "" {
		h.Close()
		return nil, fmt.Errorf("wayland hotkey: portal returned no session_handle")
	}

	// 2. BindShortcuts — the user binds the keys in the compositor settings.
	shortcuts := []portalShortcut{{
		ID:    idDictate,
		Props: shortcutProps("vole: dictate ("+nonEmpty(cfg.LangBase, "default")+")", portalTrigger(cfg.Mods, cfg.Key, false)),
	}}
	if cfg.LangShift != "" && cfg.LangShift != cfg.LangBase {
		shortcuts = append(shortcuts, portalShortcut{
			ID:    idDictateAlt,
			Props: shortcutProps("vole: dictate ("+cfg.LangShift+")", portalTrigger(cfg.Mods, cfg.Key, true)),
		})
	}
	// On KDE the first BindShortcuts pops a consent dialog; we wait for it.
	fmt.Fprintln(os.Stderr, "[vole] wayland hotkey: registering shortcuts via the "+
		"GlobalShortcuts portal — confirm the dialog on screen if it appears…")
	if _, err := h.request(obj, gsIface+".BindShortcuts", bindTimeout, nil, h.session, shortcuts, ""); err != nil {
		h.Close()
		return nil, fmt.Errorf("wayland hotkey: BindShortcuts: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[vole] wayland hotkey: bound %q/%q via GlobalShortcuts portal "+
		"(assign keys in System Settings → Shortcuts)\n", idDictate, idDictateAlt)
	return h, nil
}

// Listen dispatches Activated→onStart and Deactivated→onStop until Close.
// onLang is unused: the portal cannot report a live Shift change.
func (h *Hotkey) Listen(onStart, onLang, onStop func(lang string)) {
	_ = onLang
	for {
		select {
		case <-h.quit:
			return
		case sig, ok := <-h.signals:
			if !ok {
				return
			}
			switch sig.Name {
			case gsIface + ".Activated":
				onStart(h.langFor(shortcutID(sig)))
			case gsIface + ".Deactivated":
				onStop(h.langFor(shortcutID(sig)))
			}
		}
	}
}

// Close ends the portal session and the private bus connection.
func (h *Hotkey) Close() {
	select {
	case <-h.quit:
	default:
		close(h.quit)
	}
	if h.session != "" {
		h.conn.Object(portalBus, h.session).Call(sessionIface+".Close", 0)
		h.session = ""
	}
	h.conn.Close()
}

func (h *Hotkey) langFor(id string) string {
	if id == idDictateAlt {
		return h.cfg.LangShift
	}
	return h.cfg.LangBase
}

// request invokes a portal method that returns a request handle and blocks for
// its Response signal. timeout <= 0 waits indefinitely (until Close). fillOpts
// may add extra options; handle_token is always set. extraArgs precede the
// trailing options dict in the call.
func (h *Hotkey) request(obj dbus.BusObject, method string, timeout time.Duration,
	fillOpts func(map[string]dbus.Variant), extraArgs ...any,
) (map[string]dbus.Variant, error) {
	token := newToken("req")
	reqPath := requestPath(h.conn, token)
	// Subscribe to this request's Response before the call to avoid a race.
	if err := h.conn.AddMatchSignal(
		dbus.WithMatchObjectPath(reqPath),
		dbus.WithMatchInterface(requestIface),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return nil, err
	}

	opts := map[string]dbus.Variant{"handle_token": dbus.MakeVariant(token)}
	if fillOpts != nil {
		fillOpts(opts)
	}
	args := append(append([]any{}, extraArgs...), opts)

	var handle dbus.ObjectPath
	if err := obj.Call(method, 0, args...).Store(&handle); err != nil {
		return nil, err
	}
	return h.waitResponse(reqPath, timeout)
}

// waitResponse blocks for the Response signal at reqPath. timeout <= 0 waits
// without a deadline (until Close), as required for interactive requests.
func (h *Hotkey) waitResponse(reqPath dbus.ObjectPath, timeout time.Duration) (map[string]dbus.Variant, error) {
	var deadline <-chan time.Time // nil channel: never fires when timeout <= 0
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	for {
		select {
		case <-h.quit:
			return nil, fmt.Errorf("closed")
		case <-deadline:
			return nil, fmt.Errorf("timed out after %s (portal unavailable?)", timeout)
		case sig, ok := <-h.signals:
			if !ok {
				return nil, fmt.Errorf("signal channel closed")
			}
			if sig.Path != reqPath || sig.Name != requestIface+".Response" {
				continue // not ours (no activations expected before binding)
			}
			if len(sig.Body) < 2 {
				return nil, fmt.Errorf("malformed Response")
			}
			code, _ := sig.Body[0].(uint32)
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			if code != 0 { // 1 = cancelled, 2 = ended
				return nil, fmt.Errorf("request returned code %d", code)
			}
			return results, nil
		}
	}
}

// shortcutProps builds the (a{sv}) properties for one registered shortcut.
// trigger, when non-empty, is the suggested key combo (preferred_trigger).
func shortcutProps(description, trigger string) map[string]dbus.Variant {
	p := map[string]dbus.Variant{"description": dbus.MakeVariant(description)}
	if trigger != "" {
		p["preferred_trigger"] = dbus.MakeVariant(trigger)
	}
	return p
}

// portalTrigger renders a GlobalShortcuts preferred_trigger (e.g. "LOGO+k")
// from vole's mods ("Super") and key ("k"); withShift adds Shift (for the
// alternate-language shortcut). Returns "" if the key is not representable.
func portalTrigger(mods, key string, withShift bool) string {
	var parts []string
	seen := map[string]bool{}
	add := func(tok string) {
		if tok != "" && !seen[tok] {
			seen[tok] = true
			parts = append(parts, tok)
		}
	}
	for _, m := range strings.Split(mods, "+") {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "super", "mod4", "win", "meta", "logo":
			add("LOGO")
		case "ctrl", "control":
			add("CTRL")
		case "alt", "mod1":
			add("ALT")
		case "shift":
			add("SHIFT")
		}
	}
	if withShift {
		add("SHIFT")
	}
	k := portalKey(key)
	if k == "" {
		return ""
	}
	parts = append(parts, k)
	return strings.Join(parts, "+")
}

// portalKey maps a vole key name to a portal trigger key token. Latin
// letters/digits map directly; a few named keys are handled; others yield "".
func portalKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) == 1 {
		c := key[0]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			return key
		case c >= 'A' && c <= 'Z':
			return strings.ToLower(key)
		}
	}
	switch strings.ToLower(key) {
	case "space":
		return "space"
	case "pause":
		return "Pause"
	case "menu":
		return "Menu"
	}
	return ""
}

// shortcutID extracts the shortcut id (Body[1]) from an Activated/Deactivated signal.
func shortcutID(sig *dbus.Signal) string {
	if len(sig.Body) >= 2 {
		if id, ok := sig.Body[1].(string); ok {
			return id
		}
	}
	return ""
}

// sessionHandle reads session_handle from a CreateSession Response (string or
// object path, depending on the portal implementation).
func sessionHandle(res map[string]dbus.Variant) dbus.ObjectPath {
	v, ok := res["session_handle"]
	if !ok {
		return ""
	}
	switch x := v.Value().(type) {
	case dbus.ObjectPath:
		return x
	case string:
		return dbus.ObjectPath(x)
	}
	return ""
}

// requestPath is the well-known Request object path the portal will use for a
// call with the given handle_token: /…/request/<unique-name>/<token>, with the
// caller's bus name sanitized (leading ':' dropped, '.' → '_').
func requestPath(conn *dbus.Conn, token string) dbus.ObjectPath {
	sender := strings.TrimPrefix(conn.Names()[0], ":")
	sender = strings.ReplaceAll(sender, ".", "_")
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
}

func newToken(kind string) string {
	return fmt.Sprintf("vole_%s_%d_%d", kind, os.Getpid(), tokenSeq.Add(1))
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
