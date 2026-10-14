import QtQuick
import qs.Common
import qs.Services

// Gates activation on the backend being built. It is Go, compiled for the
// machine it runs on, so it is not shipped prebuilt. Also refuses to load
// outside a niri session, where there is nothing to switch.
QtObject {
    function check(done) {
        const dir = PluginService.getPluginPath("displaySwitch");
        if (!dir) {
            done({
                "title": I18n.trFor("displaySwitch", "Display Switch plugin directory not found"),
                "details": I18n.trFor("displaySwitch", "The plugin could not locate its own files. Try reinstalling it.")
            });
            return;
        }
        Proc.runCommand("displaySwitch.backendCheck", ["sh", "-c", 'if [ -z "$NIRI_SOCKET" ]; then echo notniri; elif [ -x "$1" ]; then echo ok; else echo nobin; fi', "sh", dir + "/bin/displayswitch-dmsd"], (stdout, exitCode) => {
            const out = (stdout || "").trim();
            if (out === "ok") {
                done(null);
                return;
            }
            if (out === "notniri") {
                done({
                    "title": I18n.trFor("displaySwitch", "Display Switch only works with niri"),
                    "details": I18n.trFor("displaySwitch", "NIRI_SOCKET is not set, so this session is not running under niri.")
                });
                return;
            }
            done({
                "title": I18n.trFor("displaySwitch", "The Display Switch backend is not built"),
                "details": I18n.trFor("displaySwitch", "Run ./build.sh in %1 (needs Go), then enable this plugin again.").arg(dir)
            });
        });
    }
}
