import QtQuick
import Quickshell
import Quickshell.Io
import qs.Common
import qs.Services
import qs.Modules.Plugins

// Daemon surface: owns the Go backend process, mirrors its state, and exposes
// the `displayswitch` IPC target used by the Super+P keybind.
PluginComponent {
    id: root

    property var popoutService: null

    // ---- settings (all optional, see DisplaySwitchSettings.qml) -------------
    readonly property string internalPrefixes: pluginData.internalPrefixes ?? ""
    readonly property string autoOnConnect: pluginData.autoOnConnect ?? "extend"
    readonly property bool restoreOnStart: pluginData.restoreOnStart ?? true
    readonly property bool reassertOnReload: pluginData.reassertOnReload ?? true
    readonly property bool confirmOnSuperRelease: pluginData.confirmOnSuperRelease ?? true
    readonly property bool showToasts: pluginData.showToasts ?? true
    readonly property string wlMirrorPath: pluginData.wlMirrorPath ?? ""

    // ---- backend state --------------------------------------------------------
    property var snap: ({
            "displays": [],
            "requested": "",
            "effective": "",
            "fallback": false,
            "available": {},
            "capabilities": {}
        })
    property bool backendUp: false
    property bool applying: false
    property int restartAttempts: 0

    // The backend is built locally by build.sh (see README).
    readonly property string bundledBinary: Qt.resolvedUrl("bin/displayswitch-dmsd").toString().replace(/^file:\/\//, "")
    readonly property var binCommand: [bundledBinary]

    readonly property var modeDefs: [
        {
            "mode": "internal",
            "icon": "laptop_mac",
            "label": I18n.trFor("displaySwitch", "PC screen only"),
            "description": I18n.trFor("displaySwitch", "Use the built-in screen only. External displays are turned off.")
        },
        {
            "mode": "external",
            "icon": "desktop_windows",
            "label": I18n.trFor("displaySwitch", "External only"),
            "description": I18n.trFor("displaySwitch", "Use external displays only. Falls back to the PC screen when they are unplugged.")
        },
        {
            "mode": "mirror",
            "icon": "cast",
            "label": I18n.trFor("displaySwitch", "Duplicate"),
            "description": I18n.trFor("displaySwitch", "Show the same picture on every display.")
        },
        {
            "mode": "extend",
            "icon": "monitor",
            "label": I18n.trFor("displaySwitch", "Extend"),
            "description": I18n.trFor("displaySwitch", "Use every display as one larger desktop.")
        }
    ]

    readonly property var modeEntries: modeDefs.map(def => {
        const av = (snap.available && snap.available[def.mode]) || {
            "available": false,
            "reason": "need_two"
        };
        return {
            "mode": def.mode,
            "icon": def.icon,
            "label": def.label,
            "description": def.description,
            "enabled": !!av.available && backendUp,
            "reason": av.reason || ""
        };
    })

    readonly property string displaysSummary: {
        const ds = snap.displays || [];
        if (!backendUp)
            return I18n.trFor("displaySwitch", "Display service unavailable");
        const on = ds.filter(d => d.enabled);
        if (ds.length === 0)
            return "";
        return on.map(d => d.label).join("  •  ");
    }

    // ---- backend process --------------------------------------------------------
    Process {
        id: backend
        running: false
        stdinEnabled: true
        command: root.binCommand.concat(["daemon", "--exit-on-stdin-eof", "--auto-on-connect", root.autoOnConnect === "keep" ? "keep" : "extend"].concat(root.restoreOnStart ? [] : ["--no-restore-on-start"]).concat(root.reassertOnReload ? [] : ["--no-reassert"]).concat(root.internalPrefixes !== "" ? ["--internal", root.internalPrefixes] : []).concat(root.wlMirrorPath !== "" ? ["--wl-mirror", root.wlMirrorPath] : []))

        stdout: SplitParser {
            onRead: line => root.handleLine(line)
        }
        stderr: SplitParser {
            onRead: line => {
                if (line.trim() !== "")
                    console.warn("displaySwitch backend:", line);
            }
        }
        onStarted: root.backendUp = true
        onExited: (exitCode, exitStatus) => {
            root.backendUp = false;
            root.applying = false;
            if (root.destroying)
                return;
            // 3: another daemon already runs (e.g. plugin reloaded): just retry later.
            // 6: not a niri session: nothing to retry.
            if (exitCode === 6) {
                ToastService.showError(I18n.trFor("displaySwitch", "Display Switch only works with niri"));
                return;
            }
            root.restartAttempts++;
            restartTimer.interval = Math.min(30000, 1000 * Math.pow(2, Math.min(root.restartAttempts, 5)));
            restartTimer.start();
        }
    }

    property bool destroying: false

    Timer {
        id: restartTimer
        repeat: false
        onTriggered: backend.running = true
    }

    // Reset the backoff once the backend has been healthy for a while.
    Timer {
        interval: 60000
        running: root.backendUp
        repeat: false
        onTriggered: root.restartAttempts = 0
    }

    Component.onCompleted: backend.running = true
    Component.onDestruction: {
        destroying = true;
        backend.running = false;
    }

    // Restart the backend when launch-relevant settings change.
    onInternalPrefixesChanged: restartBackend()
    onAutoOnConnectChanged: restartBackend()
    onWlMirrorPathChanged: restartBackend()
    property bool _settled: false
    Timer {
        interval: 500
        running: true
        onTriggered: root._settled = true
    }
    function restartBackend() {
        if (!_settled || destroying)
            return;
        backend.running = false;
        restartTimer.interval = 400;
        restartTimer.start();
    }

    // ---- state handling -------------------------------------------------------------
    function handleLine(line) {
        let ev;
        try {
            ev = JSON.parse(line);
        } catch (e) {
            console.warn("displaySwitch: bad line from backend:", line);
            return;
        }
        if (ev.type !== "state")
            return;
        applyState(ev);
        if (ev.notice)
            showNotice(ev.notice);
    }

    function applyState(s) {
        snap = s;
        if (s.effective && s.effective !== "")
            applying = false;
    }

    function modeLabel(mode) {
        for (const d of modeDefs) {
            if (d.mode === mode)
                return d.label;
        }
        return mode;
    }

    function showNotice(n) {
        if (!showToasts)
            return;
        switch (n.kind) {
        case "fallback":
            ToastService.showInfo(I18n.trFor("displaySwitch", "Display unplugged"), I18n.trFor("displaySwitch", "Switched to: %1").arg(modeLabel(n.detail)));
            break;
        case "restored":
            ToastService.showInfo(I18n.trFor("displaySwitch", "Display layout restored"), modeLabel(n.detail));
            break;
        case "connected":
            ToastService.showInfo(I18n.trFor("displaySwitch", "Display connected"), modeLabel(n.detail));
            break;
        case "mirror_failed":
            ToastService.showWarning(I18n.trFor("displaySwitch", "Mirroring stopped"), I18n.trFor("displaySwitch", "wl-mirror failed, displays are extended instead."));
            break;
        case "error":
            ToastService.showError(I18n.trFor("displaySwitch", "Display Switch"), n.detail || "");
            break;
        }
    }

    // ---- actions ----------------------------------------------------------------------
    function setMode(mode) {
        if (!backendUp) {
            ToastService.showError(I18n.trFor("displaySwitch", "Display service unavailable"));
            return;
        }
        if (applying)
            return;
        applying = true;
        Proc.runCommand("displaySwitch.set", binCommand.concat(["set", mode]), (stdout, exitCode) => {
            applying = false;
            if (exitCode === 0) {
                try {
                    const r = JSON.parse(stdout.trim().split("\n").pop());
                    if (r.state)
                        applyState(r.state);
                    if (r.message)
                        ToastService.showWarning(I18n.trFor("displaySwitch", "Display Switch"), r.message);
                } catch (e) {}
                modal.close();
                return;
            }
            if (exitCode === 5)
                ToastService.showError(I18n.trFor("displaySwitch", "Install wl-mirror to mirror displays"));
            else if (exitCode === 4)
                ToastService.showWarning(I18n.trFor("displaySwitch", "This mode is not available with the connected displays"));
            else
                ToastService.showError(I18n.trFor("displaySwitch", "Could not change the display layout"), (stdout || "").trim());
            // The list of connected displays may have changed under us.
            refresh();
        }, 0, 25000, root);
    }

    function refresh() {
        Proc.runCommand("displaySwitch.status", binCommand.concat(["status"]), (stdout, exitCode) => {
            if (exitCode !== 0)
                return;
            try {
                const r = JSON.parse(stdout.trim().split("\n").pop());
                if (r.state)
                    applyState(r.state);
            } catch (e) {}
        }, 0, 8000, root);
    }

    function rescue() {
        Proc.runCommand("displaySwitch.rescue", binCommand.concat(["rescue"]), (stdout, exitCode) => {
            if (exitCode === 0)
                ToastService.showInfo(I18n.trFor("displaySwitch", "All displays enabled"));
            else
                ToastService.showError(I18n.trFor("displaySwitch", "Could not enable the displays"));
            refresh();
        }, 0, 15000, root);
    }

    function openPopup() {
        if (!backendUp) {
            ToastService.showError(I18n.trFor("displaySwitch", "Display service unavailable"));
            return;
        }
        refresh();
        modal.prepare();
        modal.open();
    }

    // Super+P pressed while the popup is open cycles, like GNOME.
    function openOrCycle() {
        if (modal.shouldBeVisible) {
            modal.moveSelection(1);
            modal.changedSinceOpen = true;
        } else {
            openPopup();
        }
    }

    DisplaySwitchModal {
        id: modal
        modes: root.modeEntries
        currentMode: root.snap.effective || ""
        fallbackActive: !!root.snap.fallback
        busy: root.applying
        confirmOnSuperRelease: root.confirmOnSuperRelease
        displaysSummary: root.displaysSummary
        onModeChosen: mode => root.setMode(mode)
    }

    IpcHandler {
        target: "displayswitch"

        function open(): string {
            root.openPopup();
            return "ok";
        }
        function close(): string {
            modal.close();
            return "ok";
        }
        function toggle(): string {
            root.openOrCycle();
            return "ok";
        }
        function cycle(): string {
            root.openOrCycle();
            return "ok";
        }
        function set(mode: string): string {
            if (["internal", "external", "mirror", "extend"].indexOf(mode) < 0)
                return "ERROR: unknown mode (internal|external|mirror|extend)";
            root.setMode(mode);
            return "ok";
        }
        function status(): string {
            return JSON.stringify({
                "backend": root.backendUp,
                "requested": root.snap.requested,
                "effective": root.snap.effective,
                "fallback": root.snap.fallback,
                "displays": (root.snap.displays || []).map(d => d.name + (d.enabled ? "*" : ""))
            });
        }
        function rescue(): string {
            root.rescue();
            return "ok";
        }
    }
}
