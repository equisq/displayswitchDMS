import QtQuick
import qs.Common
import qs.Modals.Common
import qs.Widgets

// Centered popup, modelled on GNOME's Super+P switcher and styled like DMS's
// own modals (PowerProfileModal). All state comes from the daemon component.
DankModal {
    id: root

    layerNamespace: "dms:display-switch"
    keepPopoutsOpen: true

    // ---- inputs (bound by DisplaySwitchDaemon.qml) ------------------------
    property var modes: []              // [{mode, icon, label, description, enabled, reason}]
    property string currentMode: ""     // mode in effect right now
    property bool fallbackActive: false // requested mode could not be honoured
    property bool busy: false
    property bool confirmOnSuperRelease: true
    property string displaysSummary: ""

    // ---- ui state -----------------------------------------------------------
    property int selectedIndex: 0
    property bool changedSinceOpen: false

    signal modeChosen(string mode)

    readonly property var selectedEntry: (selectedIndex >= 0 && selectedIndex < modes.length) ? modes[selectedIndex] : null

    shouldBeVisible: false
    modalWidth: Math.max(520, Math.min(760, modes.length * 168 + Theme.spacingL * 2 + Theme.spacingM * Math.max(0, modes.length - 1)))
    modalHeight: 318 + (fallbackActive ? 44 : 0)
    enableShadow: true
    onBackgroundClicked: close()

    function reasonText(reason) {
        switch (reason) {
        case "no_wl_mirror":
            return I18n.trFor("displaySwitch", "Install wl-mirror to mirror displays");
        case "no_external":
            return I18n.trFor("displaySwitch", "No external display connected");
        case "no_internal":
            return I18n.trFor("displaySwitch", "No built-in display");
        default:
            return I18n.trFor("displaySwitch", "At least two displays are needed");
        }
    }

    function indexOfMode(mode) {
        for (let i = 0; i < modes.length; i++) {
            if (modes[i].mode === mode)
                return i;
        }
        return -1;
    }

    // Called by the daemon right before open().
    function prepare() {
        const i = indexOfMode(currentMode);
        selectedIndex = i >= 0 ? i : firstEnabled();
        changedSinceOpen = false;
    }

    function firstEnabled() {
        for (let i = 0; i < modes.length; i++) {
            if (modes[i].enabled)
                return i;
        }
        return 0;
    }

    // Moves the selection to the next/previous mode that can be applied.
    function moveSelection(delta) {
        const n = modes.length;
        if (n === 0)
            return;
        let idx = selectedIndex;
        for (let step = 0; step < n; step++) {
            idx = (idx + delta + n) % n;
            if (modes[idx].enabled) {
                selectedIndex = idx;
                changedSinceOpen = true;
                return;
            }
        }
    }

    function chooseIndex(i) {
        if (busy || i < 0 || i >= modes.length || !modes[i].enabled)
            return;
        selectedIndex = i;
        modeChosen(modes[i].mode);
    }

    // The list can shrink while the popup is open (a monitor was unplugged).
    onModesChanged: {
        if (modes.length === 0)
            return;
        if (selectedIndex >= modes.length)
            selectedIndex = modes.length - 1;
        if (!modes[selectedIndex].enabled)
            selectedIndex = firstEnabled();
    }

    onShouldHaveFocusChanged: {
        if (!shouldHaveFocus)
            return;
        Qt.callLater(() => modalFocusScope.forceActiveFocus());
    }

    modalFocusScope.Keys.onPressed: event => {
        if (event.isAutoRepeat && event.key !== Qt.Key_Left && event.key !== Qt.Key_Right) {
            event.accepted = true;
            return;
        }
        if (busy) {
            if (event.key === Qt.Key_Escape)
                close();
            event.accepted = true;
            return;
        }
        switch (event.key) {
        case Qt.Key_Left:
        case Qt.Key_Up:
        case Qt.Key_Backtab:
            moveSelection(-1);
            event.accepted = true;
            return;
        case Qt.Key_Right:
        case Qt.Key_Down:
        case Qt.Key_Tab:
            moveSelection(1);
            event.accepted = true;
            return;
        case Qt.Key_P:
            // Same as pressing Super+P again in GNOME: cycle.
            moveSelection((event.modifiers & Qt.ShiftModifier) ? -1 : 1);
            event.accepted = true;
            return;
        case Qt.Key_Space:
        case Qt.Key_Return:
        case Qt.Key_Enter:
            chooseIndex(selectedIndex);
            event.accepted = true;
            return;
        case Qt.Key_Escape:
            close();
            event.accepted = true;
            return;
        }
        if (event.key >= Qt.Key_1 && event.key <= Qt.Key_9) {
            chooseIndex(event.key - Qt.Key_1);
            event.accepted = true;
        }
    }

    // GNOME behaviour: after cycling with Super+P, letting go of Super applies.
    modalFocusScope.Keys.onReleased: event => {
        const isSuper = event.key === Qt.Key_Super_L || event.key === Qt.Key_Super_R || event.key === Qt.Key_Meta;
        if (isSuper && confirmOnSuperRelease && changedSinceOpen && !busy) {
            chooseIndex(selectedIndex);
            event.accepted = true;
        }
    }

    content: Component {
        Item {
            anchors.fill: parent

            Column {
                anchors.fill: parent
                anchors.margins: Theme.spacingL
                spacing: Theme.spacingL

                Row {
                    width: parent.width

                    Column {
                        width: parent.width - 40
                        spacing: Theme.spacingXS

                        StyledText {
                            text: I18n.trFor("displaySwitch", "Display")
                            font.pixelSize: Theme.fontSizeLarge
                            color: Theme.surfaceText
                            font.weight: Font.Medium
                        }

                        StyledText {
                            text: root.displaysSummary !== "" ? root.displaysSummary : I18n.trFor("displaySwitch", "Choose how to use your displays")
                            font.pixelSize: Theme.fontSizeSmall
                            color: Theme.surfaceTextMedium
                            width: parent.width
                            elide: Text.ElideRight
                        }
                    }

                    DankActionButton {
                        iconName: "close"
                                        iconSize: Theme.iconSize - 4
                        iconColor: Theme.surfaceText
                        onClicked: root.close()
                    }
                }

                // Shown when a requested layout could not be kept (cable pulled...).
                Rectangle {
                    visible: root.fallbackActive
                    width: parent.width
                    height: visible ? 36 : 0
                    radius: Theme.cornerRadius
                    color: Theme.surfaceHover

                    Row {
                        anchors.centerIn: parent
                        spacing: Theme.spacingS

                        DankIcon {
                            name: "info"
                            size: Theme.fontSizeMedium
                            color: Theme.surfaceText
                            anchors.verticalCenter: parent.verticalCenter
                        }

                        StyledText {
                            text: I18n.trFor("displaySwitch", "Display unplugged: using the remaining screen")
                            font.pixelSize: Theme.fontSizeSmall
                            color: Theme.surfaceText
                            anchors.verticalCenter: parent.verticalCenter
                        }
                    }
                }

                Row {
                    id: buttonsRow
                    width: parent.width
                    spacing: Theme.spacingM
                    opacity: root.busy ? 0.55 : 1.0

                    Behavior on opacity {
                        NumberAnimation {
                            duration: Theme.shortDuration
                        }
                    }

                    Repeater {
                        model: root.modes

                        Rectangle {
                            id: card
                            required property int index
                            required property var modelData

                            readonly property bool isSelected: root.selectedIndex === index
                            readonly property bool isActive: root.currentMode === modelData.mode
                            readonly property bool isEnabled: modelData.enabled

                            width: (buttonsRow.width - Theme.spacingM * (root.modes.length - 1)) / root.modes.length
                            height: 120
                            radius: Theme.cornerRadius
                            opacity: isEnabled ? 1.0 : 0.45

                            color: {
                                if (isActive)
                                    return Theme.primaryPressed;
                                if (isSelected && isEnabled)
                                    return Theme.primaryHoverLight;
                                if (mouseArea.containsMouse && isEnabled)
                                    return Theme.surfacePressed;
                                return Theme.surfaceHover;
                            }

                            border.color: isActive ? Theme.primary : (isSelected ? Theme.withAlpha(Theme.primary, 0.5) : Theme.withAlpha(Theme.primary, 0))
                            border.width: (isActive || isSelected) ? 2 : 0

                            // Shortcut key badge (1-4), same as the power profile popup.
                            Rectangle {
                                anchors.top: parent.top
                                anchors.right: parent.right
                                anchors.margins: Theme.spacingS
                                width: 20
                                height: 20
                                radius: 4
                                color: card.isActive ? Theme.primaryPressed : Theme.surfaceTextHover

                                StyledText {
                                    text: (card.index + 1).toString()
                                    font.pixelSize: Theme.fontSizeSmall
                                    font.weight: Font.Bold
                                    color: card.isActive ? Theme.primary : Theme.surfaceTextMedium
                                    anchors.centerIn: parent
                                }
                            }

                            Column {
                                anchors.centerIn: parent
                                width: parent.width - Theme.spacingS * 2
                                spacing: Theme.spacingS

                                DankIcon {
                                    name: card.modelData.icon
                                    size: Theme.iconSize + 16
                                    color: card.isActive ? Theme.primary : Theme.surfaceText
                                    filled: card.isActive
                                    anchors.horizontalCenter: parent.horizontalCenter
                                }

                                StyledText {
                                    text: card.modelData.label
                                    width: parent.width
                                    font.pixelSize: Theme.fontSizeMedium
                                    color: card.isActive ? Theme.primary : Theme.surfaceText
                                    font.weight: Font.Medium
                                    horizontalAlignment: Text.AlignHCenter
                                    wrapMode: Text.WordWrap
                                    maximumLineCount: 2
                                    elide: Text.ElideRight
                                }
                            }

                            MouseArea {
                                id: mouseArea
                                anchors.fill: parent
                                hoverEnabled: true
                                enabled: !root.busy
                                cursorShape: card.isEnabled ? Qt.PointingHandCursor : Qt.ArrowCursor
                                onEntered: {
                                    if (card.isEnabled)
                                        root.selectedIndex = card.index;
                                }
                                onClicked: root.chooseIndex(card.index)
                            }
                        }
                    }
                }

                StyledText {
                    text: {
                        if (root.busy)
                            return I18n.trFor("displaySwitch", "Applying…");
                        const e = root.selectedEntry;
                        if (!e)
                            return "";
                        return e.enabled ? e.description : root.reasonText(e.reason);
                    }
                    font.pixelSize: Theme.fontSizeMedium
                    color: Theme.surfaceTextMedium
                    horizontalAlignment: Text.AlignHCenter
                    anchors.horizontalCenter: parent.horizontalCenter
                    wrapMode: Text.WordWrap
                    width: parent.width - Theme.spacingL * 2
                }

                Row {
                    anchors.horizontalCenter: parent.horizontalCenter
                    spacing: Theme.spacingXS
                    opacity: 0.5

                    DankIcon {
                        name: "keyboard"
                        size: Theme.fontSizeSmall
                        color: Theme.surfaceText
                    }

                    StyledText {
                        text: I18n.trFor("displaySwitch", "Use keys 1-4, arrows or P to cycle, Enter to select")
                        font.pixelSize: Theme.fontSizeSmall - 1
                        color: Theme.surfaceText
                    }
                }
            }
        }
    }
}
