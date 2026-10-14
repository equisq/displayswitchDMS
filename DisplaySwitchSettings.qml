import QtQuick
import qs.Common
import qs.Modules.Plugins
import qs.Widgets

PluginSettings {
    id: root
    pluginId: "displaySwitch"

    StyledText {
        width: parent.width
        text: I18n.trFor("displaySwitch", "Display Switch")
        font.pixelSize: Theme.fontSizeLarge
        font.weight: Font.Bold
        color: Theme.surfaceText
    }

    StyledText {
        width: parent.width
        text: I18n.trFor("displaySwitch", "Assign a shortcut in Settings → Keyboard Shortcuts with the Shell Command: dms ipc call displayswitch toggle")
        font.pixelSize: Theme.fontSizeSmall
        color: Theme.surfaceVariantText
        wrapMode: Text.WordWrap
    }

    SelectionSetting {
        settingKey: "autoOnConnect"
        label: I18n.trFor("displaySwitch", "When a new display is plugged in")
        description: I18n.trFor("displaySwitch", "Displays you already configured keep their saved layout.")
        options: [
            {
                "label": I18n.trFor("displaySwitch", "Extend the desktop"),
                "value": "extend"
            },
            {
                "label": I18n.trFor("displaySwitch", "Keep the current mode"),
                "value": "keep"
            }
        ]
        defaultValue: "extend"
    }

    ToggleSetting {
        settingKey: "restoreOnStart"
        label: I18n.trFor("displaySwitch", "Restore layout at login")
        description: I18n.trFor("displaySwitch", "Re-apply the last mode you chose for the connected displays.")
        defaultValue: true
    }

    ToggleSetting {
        settingKey: "reassertOnReload"
        label: I18n.trFor("displaySwitch", "Keep layout after niri config reload")
        description: I18n.trFor("displaySwitch", "niri forgets temporary output changes when its config reloads.")
        defaultValue: true
    }

    ToggleSetting {
        settingKey: "confirmOnSuperRelease"
        label: I18n.trFor("displaySwitch", "Apply when the Super key is released")
        description: I18n.trFor("displaySwitch", "Like GNOME: press the shortcut repeatedly to cycle, release Super to apply.")
        defaultValue: true
    }

    ToggleSetting {
        settingKey: "showToasts"
        label: I18n.trFor("displaySwitch", "Show notifications")
        description: I18n.trFor("displaySwitch", "Tell me when a display is unplugged or restored.")
        defaultValue: true
    }

    StringSetting {
        settingKey: "internalPrefixes"
        label: I18n.trFor("displaySwitch", "Built-in display names")
        description: I18n.trFor("displaySwitch", "Comma separated connector prefixes. Empty means eDP, LVDS, DSI.")
        placeholder: "eDP,LVDS,DSI"
        defaultValue: ""
    }

    StringSetting {
        settingKey: "wlMirrorPath"
        label: I18n.trFor("displaySwitch", "wl-mirror path")
        description: I18n.trFor("displaySwitch", "Only needed if wl-mirror is not in your PATH.")
        placeholder: "wl-mirror"
        defaultValue: ""
    }
}
